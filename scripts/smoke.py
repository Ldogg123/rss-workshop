#!/usr/bin/env python3
"""Exercise the compiled server and local fixture using only Python's standard library."""
import datetime
import http.cookiejar
import http.server
import io
import json
import os
import pathlib
import secrets
import shlex
import shutil
import socket
import subprocess
import tarfile
import tempfile
import threading
import time
import urllib.error
import urllib.request
import urllib.parse
import xml.etree.ElementTree as ET

ROOT = pathlib.Path(__file__).resolve().parents[1]
fixture = (ROOT / 'testdata/cards.html').read_bytes()
state = {'body': fixture, 'requests': 0}
ATOM_NS = '{http://www.w3.org/2005/Atom}'


def atom_items(body):
    """Parse the reader output independently from the Go serializer."""
    root = ET.fromstring(body)
    assert root.tag == ATOM_NS + 'feed', root.tag
    assert all(element.tag.startswith(ATOM_NS) for element in root.iter()), 'Element outside Atom namespace'
    assert root.findtext(ATOM_NS + 'id')
    assert root.findtext(ATOM_NS + 'author/' + ATOM_NS + 'name')
    datetime.datetime.fromisoformat(root.findtext(ATOM_NS + 'updated'))
    identities = {}
    for entry in root.findall(ATOM_NS + 'entry'):
        links = [link for link in entry.findall(ATOM_NS + 'link') if link.get('rel') == 'alternate']
        assert len(links) == 1
        link = links[0].get('href')
        parsed = urllib.parse.urlparse(link)
        assert parsed.scheme in ('http', 'https') and parsed.netloc, link
        guid = entry.findtext(ATOM_NS + 'id')
        assert guid and link not in identities
        identities[link] = guid
        datetime.datetime.fromisoformat(entry.findtext(ATOM_NS + 'published'))
        datetime.datetime.fromisoformat(entry.findtext(ATOM_NS + 'updated'))
        content = entry.find(ATOM_NS + 'content')
        assert content is not None and content.get('type') == 'html' and len(content) == 0
    return root, identities


class Fixture(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        state['requests'] += 1
        self.send_response(200)
        self.send_header('Content-Type', 'text/html')
        self.end_headers()
        self.wfile.write(state['body'])

    def log_message(self, *args):
        pass


def wait_for(fn):
    deadline = time.monotonic() + 12
    while time.monotonic() < deadline:
        try:
            result = fn()
            if result:
                return result
        except (urllib.error.URLError, ConnectionError):
            pass
        time.sleep(0.1)
    raise AssertionError('Timed out waiting for smoke-test condition')


def smoke_environment(tmp, password, base, port, postgres_mode, compose_mode):
    # Never inherit an operator's DATABASE_URL, including for ordinary Compose
    # tests where shell variables would override the temporary --env-file.
    database_url = ''
    if postgres_mode and not compose_mode:
        database_url = os.environ.get('RSS_TEST_POSTGRES_URL', '')
        if not database_url:
            raise ValueError('Native PostgreSQL smoke requires RSS_TEST_POSTGRES_URL pointing to an empty disposable test database')
    return dict(os.environ, ADMIN_PASSWORD=password, ADMIN_PASSWORD_HASH='', DATA_DIR=tmp,
                RSS_DATA_DIR=str(pathlib.Path(tmp) / 'data'),
                POSTGRES_DATA_DIR=str(pathlib.Path(tmp) / 'postgres-data'),
                DATABASE_URL=database_url, PUBLIC_BASE_URL=base, LISTEN_ADDR=f'127.0.0.1:{port}',
                ALLOW_CIDRS='127.0.0.1/32', STATIC_WORKERS='2', FETCH_TIMEOUT='5s')


def main():
    global fixture
    browser_mode = os.environ.get('RSS_SMOKE_BROWSER') == '1'
    if browser_mode:
        cards = '<article><h2>First &amp; best</h2><a href="/one">Read</a><img data-lazy-src="/thumb.jpg"></article><article><h2>Second story</h2><a href="/two">Read</a></article>'
        fixture = ('<html><body><script>setTimeout(()=>{document.body.innerHTML=' + json.dumps(cards) + '},120)</script></body></html>').encode()
        state['body'] = fixture
    compose_mode = os.environ.get('RSS_SMOKE_COMPOSE') == '1'
    postgres_mode = os.environ.get('RSS_SMOKE_POSTGRES') == '1'
    if postgres_mode and not compose_mode and not os.environ.get('RSS_TEST_POSTGRES_URL'):
        raise ValueError('Native PostgreSQL smoke requires RSS_TEST_POSTGRES_URL pointing to an empty disposable test database')
    source = http.server.ThreadingHTTPServer(('0.0.0.0' if compose_mode else '127.0.0.1', 0), Fixture)
    threading.Thread(target=source.serve_forever, daemon=True).start()
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    base = f'http://127.0.0.1:{port}'
    password = secrets.token_urlsafe(24)
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
    csrf = ''

    def request(path, method='GET', data=None, headers=None):
        h = {'Origin': base, 'Content-Type': 'application/json', 'X-CSRF-Token': csrf}
        h.update(headers or {})
        req = urllib.request.Request(base + path, data=None if data is None else json.dumps(data).encode(), headers=h, method=method)
        try:
            with opener.open(req, timeout=3) as r:
                return r.status, r.read(), dict(r.headers)
        except urllib.error.HTTPError as e:
            return e.code, e.read(), dict(e.headers)

    def data(path):
        code, body, _ = request(path)
        assert code == 200, (code, body)
        return json.loads(body)

    with tempfile.TemporaryDirectory(prefix='rss-smoke-') as tmp:
        env = smoke_environment(tmp, password, base, port, postgres_mode, compose_mode)
        compose = None
        source_host = '127.0.0.1'
        if compose_mode:
            docker = shlex.split(os.environ.get('DOCKER', 'docker'))
            if not docker:
                raise ValueError('DOCKER must name a Docker command, for example docker or sudo docker')
            gateway = subprocess.check_output(docker + ['network', 'inspect', 'bridge', '--format', '{{(index .IPAM.Config 0).Gateway}}'], text=True).strip()
            env['ALLOW_CIDRS'] = gateway + '/32'
            source_host = 'host.docker.internal'
            project = 'rss-smoke-' + secrets.token_hex(4)
            override = pathlib.Path(tmp) / 'override.yaml'
            image = os.environ.get('RSS_SMOKE_IMAGE', 'rss-workshop:browser-check' if browser_mode else 'rss-workshop:static-check')
            override.write_text('services:\n  rss-workshop:\n    image: ' + image + '\n    ports: !override\n      - "127.0.0.1:' + str(port) + ':8080"\n    extra_hosts:\n      - "host.docker.internal:host-gateway"\n')
            env_file = pathlib.Path(tmp) / 'compose.env'
            env_keys = ['ADMIN_PASSWORD', 'ADMIN_PASSWORD_HASH', 'PUBLIC_BASE_URL', 'ALLOW_CIDRS', 'STATIC_WORKERS', 'FETCH_TIMEOUT', 'DATABASE_URL', 'RSS_DATA_DIR', 'POSTGRES_DATA_DIR']
            if postgres_mode:
                env['POSTGRES_PASSWORD'] = secrets.token_hex(24)
                env['DATABASE_URL'] = 'postgres://rss_workshop:' + env['POSTGRES_PASSWORD'] + '@postgres:5432/rss_workshop?sslmode=disable'
                env_keys.append('POSTGRES_PASSWORD')
            env_file.write_text(''.join(k + '=' + env[k] + '\n' for k in env_keys))
            env_file.chmod(0o600)
            compose = docker + ['compose', '--env-file', str(env_file), '-p', project, '-f', str(ROOT / 'compose.yaml')]
            # The browser workflow exercises the default installation. Static
            # fixtures explicitly opt out, including the PostgreSQL matrix.
            if not browser_mode:
                compose += ['-f', str(ROOT / 'compose.static.yaml')]
            if postgres_mode:
                compose += ['-f', str(ROOT / 'compose.postgres.yaml')]
            compose += ['-f', str(override)]
        proc = None
        storage_helper = None
        created_ids = []
        with open(pathlib.Path(tmp) / 'server.log', 'wb') as log:
            def compose_run(*args, **kwargs):
                return subprocess.run(compose + list(args), env=env, check=True,
                                      stderr=log, **kwargs)

            def data_directory_mode(mode):
                # Docker applies ownership inside its own user namespace, so
                # this also avoids assuming the host UID mapping of a daemon.
                archive = io.BytesIO()
                with tarfile.open(fileobj=archive, mode='w') as bundle:
                    directory = tarfile.TarInfo('data')
                    directory.type = tarfile.DIRTYPE
                    directory.mode = mode
                    directory.uid = directory.gid = 65532
                    bundle.addfile(directory)
                subprocess.run(docker + ['cp', '--archive', '-', storage_helper + ':/fixture'],
                               input=archive.getvalue(), check=True, stdout=log, stderr=log)

            def prepare_storage():
                nonlocal storage_helper
                pathlib.Path(env['RSS_DATA_DIR']).mkdir(mode=0o700)
                if postgres_mode:
                    # PostgreSQL 18 owns/chmods its nested PGDATA, but its
                    # unprivileged process must traverse this mounted parent.
                    postgres_data = pathlib.Path(env['POSTGRES_DATA_DIR'])
                    postgres_data.mkdir(mode=0o755)
                    postgres_data.chmod(0o755)
                storage_helper = subprocess.run(docker + ['create', '--pull', 'never', '--name', project + '-storage',
                    '--label', 'rss-workshop.smoke=' + project, '--network', 'none', '--read-only',
                    '--entrypoint', '/rss-workshop', '--mount', 'type=bind,source=' + tmp + ',target=/fixture',
                    image], check=True, stdout=subprocess.PIPE, stderr=log).stdout.decode().strip()
                data_directory_mode(0o700)

            def cleanup_storage():
                # Services have stopped. Every path below was generated within
                # this run's private 0700 temp directory, never taken from env.
                if postgres_mode:
                    postgres_data = pathlib.Path(env['POSTGRES_DATA_DIR'])
                    if postgres_data.exists():
                        try:
                            shutil.rmtree(postgres_data)
                        except PermissionError:
                            # PostgreSQL initializes nested directories as its
                            # own UID. Its existing image can remove these test
                            # files without host sudo or another helper image.
                            subprocess.run(docker + ['run', '--rm', '--pull', 'never', '--network', 'none',
                                '--read-only', '--user', '0', '--entrypoint', '/bin/sh',
                                '--mount', 'type=bind,source=' + str(postgres_data) + ',target=/fixture',
                                'postgres:18-alpine', '-c', 'rm -rf /fixture/* /fixture/.[!.]* /fixture/..?*'],
                                check=True, stdout=log, stderr=log)
                            postgres_data.rmdir()
                if storage_helper:
                    try:
                        # The private parent remains 0700. Temporarily permit
                        # host cleanup even when daemon UID mapping differs.
                        data_directory_mode(0o777)
                        shutil.rmtree(env['RSS_DATA_DIR'])
                    finally:
                        subprocess.run(docker + ['rm', storage_helper], check=True, stdout=log, stderr=log)

            def stop():
                if compose:
                    compose_run('stop', 'rss-workshop', stdout=log)
                elif proc is not None and proc.poll() is None:
                    proc.terminate()
                    assert proc.wait(timeout=10) == 0

            def start():
                nonlocal proc
                if compose:
                    # sudo Docker commands may discard the process environment;
                    # keep the explicit private Compose env file authoritative.
                    env_file.write_text(''.join(k + '=' + env[k] + '\n' for k in env_keys))
                    subprocess.run(compose + ['up', '--no-build', '-d', '--wait'], env=env, check=True, stdout=log, stderr=log)
                    expected_mounts = [('rss-workshop', '/data', env['RSS_DATA_DIR'])]
                    if postgres_mode:
                        expected_mounts.append(('postgres', '/var/lib/postgresql', env['POSTGRES_DATA_DIR']))
                    for service, destination, host_path in expected_mounts:
                        container = compose_run('ps', '-q', service, stdout=subprocess.PIPE).stdout.decode().strip()
                        mounts = json.loads(subprocess.run(docker + ['inspect', '--format', '{{json .Mounts}}', container],
                            check=True, stdout=subprocess.PIPE, stderr=log).stdout)
                        mount = next((item for item in mounts if item['Destination'] == destination), None)
                        assert mount and mount['Type'] == 'bind' and pathlib.Path(mount['Source']).resolve() == pathlib.Path(host_path).resolve(), 'Compose did not use its isolated test host directory'
                else:
                    proc = subprocess.Popen([str(ROOT / 'bin/rss-workshop')], env=env, stdout=log, stderr=log)
                wait_for(lambda: request('/readyz')[0] == 200)

            def login():
                nonlocal csrf
                code, body, _ = request('/api/login', 'POST', {'password': password})
                assert code == 200, (code, body)
                csrf = json.loads(body)['csrf']

            try:
                if compose:
                    prepare_storage()
                start()
                subprocess.run((compose + ['exec', '-T', 'rss-workshop', '/rss-workshop', '-healthcheck']) if compose else [str(ROOT / 'bin/rss-workshop'), '-healthcheck'], env=env, check=True)
                assert request('/')[0] == 200
                assert request('/assets/app.js')[0] == 200
                login()
                assert not data('/api/feeds')['feeds'], 'Smoke tests require an empty disposable database; existing feeds were left unchanged'
                recipe = {'title': 'Smoke fixture', 'url': f'http://{source_host}:{source.server_port}/cards.html',
                          'interval': 60, 'enabled': True,
                          'recipe': {'mode': 'auto' if browser_mode else 'static', 'wait_selector': 'article' if browser_mode else '', 'type': 'css', 'items': 'article', 'title': {'selector': 'h2'},
                                     'link': {'selector': 'a'}, 'image': {'selector': 'img'},
                                     'content': {'selector': '.summary'}}}
                code, preview, _ = request('/api/preview', 'POST', recipe)
                assert code == 200 and len(json.loads(preview)['items']) == 2
                code, saved, _ = request('/api/feeds', 'POST', recipe)
                assert code == 200, saved
                feed_id = json.loads(saved)['id']
                created_ids.append(feed_id)
                wait_for(lambda: data('/api/feeds')['feeds'][0]['count'] == 2)
                saved_feed = data('/api/feeds')['feeds'][0]
                rss_path = urllib.parse.urlparse(saved_feed['rss_url']).path
                atom_path = urllib.parse.urlparse(saved_feed['atom_url']).path
                code, original, headers = request(rss_path)
                assert code == 200
                root = ET.fromstring(original)
                guids = {item.findtext('link'): item.findtext('guid') for item in root.findall('./channel/item')}
                assert len(guids) == 2
                code, original_atom, atom_headers = request(atom_path)
                assert code == 200 and atom_headers['Content-Type'].startswith('application/atom+xml')
                atom_root, atom_guids = atom_items(original_atom)
                assert atom_guids == guids, 'Atom IDs differ from RSS GUIDs'
                atom_feed_id = atom_root.findtext(ATOM_NS + 'id')
                self_links = [link for link in atom_root.findall(ATOM_NS + 'link') if link.get('rel') == 'self']
                assert len(self_links) == 1 and self_links[0].get('href') == saved_feed['atom_url']
                requests_before = state['requests']
                for _ in range(5):
                    assert request(rss_path)[1] == original
                    assert request(atom_path)[1] == original_atom
                assert state['requests'] == requests_before, 'Reader request triggered source fetch'
                assert request(rss_path, headers={'If-None-Match': headers['Etag']})[0] == 304
                assert request(atom_path, headers={'If-None-Match': atom_headers['Etag']})[0] == 304
                assert state['requests'] == requests_before, 'Conditional reader request triggered source fetch'
                state['body'] = fixture.replace(b'First &amp; best', b'Updated story')
                assert request(f'/api/feeds/{feed_id}/refresh', 'POST', {})[0] == 202
                wait_for(lambda: b'Updated story' in request(rss_path)[1])
                updated = request(rss_path)[1]
                root = ET.fromstring(updated)
                assert {item.findtext('link'): item.findtext('guid') for item in root.findall('./channel/item')} == guids
                code, updated_atom, updated_atom_headers = request(atom_path)
                assert code == 200 and b'Updated story' in updated_atom
                atom_root, updated_atom_guids = atom_items(updated_atom)
                assert updated_atom_guids == guids and atom_root.findtext(ATOM_NS + 'id') == atom_feed_id
                assert updated_atom_headers['Etag'] != atom_headers['Etag'], 'Atom ETag did not reflect the edit'
                state['body'] = b'<html>No articles today</html>'
                assert request(f'/api/feeds/{feed_id}/refresh', 'POST', {})[0] == 202
                wait_for(lambda: data('/api/feeds')['feeds'][0]['error'])
                assert request(rss_path)[1] == updated, 'Empty scrape changed saved output'
                code, retained_atom, retained_atom_headers = request(atom_path)
                assert code == 200 and retained_atom == updated_atom, 'Empty scrape changed saved Atom output'
                assert retained_atom_headers['Etag'] == updated_atom_headers['Etag']
                stop()
                start()
                assert request('/api/feeds')[0] == 401, 'Restart failed to invalidate session'
                assert request(rss_path)[1] == updated, 'Restart lost persisted output'
                code, restarted_atom, restarted_atom_headers = request(atom_path)
                assert code == 200 and restarted_atom == updated_atom, 'Restart lost persisted Atom output'
                assert restarted_atom_headers['Etag'] == updated_atom_headers['Etag']
                login()
                assert data('/api/feeds')['feeds'][0]['count'] == 2

                # Import only after the single-feed checks above; copies may sort
                # ahead of the original and must be addressed by their own IDs.
                original_feed = next(f for f in data('/api/feeds')['feeds'] if f['id'] == feed_id)
                requests_before = state['requests']
                code, exported, export_headers = request('/api/recipes/export?' + urllib.parse.urlencode({'id': feed_id}))
                assert code == 200 and export_headers['Content-Type'].startswith('application/json')
                assert 'attachment;' in export_headers['Content-Disposition']
                document = json.loads(exported)
                assert document['format'] == 'rss-workshop.recipes' and document['version'] == 1
                assert len(document['feeds']) == 1
                portable = document['feeds'][0]
                assert set(portable) == {'title', 'url', 'interval', 'recipe'}, 'Export included runtime or private fields'
                for field in portable:
                    assert portable[field] == original_feed[field], f'Export changed {field}'
                original_token = rss_path.rsplit('/', 1)[1].removesuffix('.xml')
                assert feed_id.encode() not in exported and original_token.encode() not in exported
                code, validated, _ = request('/api/recipes/preview', 'POST', document)
                assert code == 200, validated
                validation = json.loads(validated)
                assert validation['count'] == 1 and validation['feeds'] == document['feeds']
                assert len(data('/api/feeds')['feeds']) == 1, 'Preview saved a recipe'
                code, imported, _ = request('/api/recipes/import', 'POST', document)
                assert code == 201, imported
                result = json.loads(imported)
                created_ids.extend(result['ids'])
                assert result['count'] == 1 and len(result['ids']) == 1
                copy_id = result['ids'][0]
                assert copy_id != feed_id
                all_feeds = {f['id']: f for f in data('/api/feeds')['feeds']}
                assert len(all_feeds) == 2 and all_feeds[feed_id] == original_feed, 'Import changed the original feed'
                copy = all_feeds[copy_id]
                assert copy['enabled'] is False and copy['count'] == 0, 'Imported feed was not paused and empty'
                for field in portable:
                    assert copy[field] == portable[field], f'Import changed {field}'
                copy_rss_path = urllib.parse.urlparse(copy['rss_url']).path
                copy_atom_path = urllib.parse.urlparse(copy['atom_url']).path
                copy_token = copy_rss_path.rsplit('/', 1)[1].removesuffix('.xml')
                assert copy_token != original_token and copy_token != copy_id
                assert copy_atom_path.rsplit('/', 1)[1].removesuffix('.atom') == copy_token
                assert request(copy_rss_path)[0] == 503 and request(copy_atom_path)[0] == 503
                assert state['requests'] == requests_before, 'Recipe portability or paused readers fetched the source'
                assert request(rss_path)[1] == updated and request(atom_path)[1] == updated_atom
                if postgres_mode and compose:
                    # This project owns its PostgreSQL service, so interruption
                    # and restore never target RSS_TEST_POSTGRES_URL or a real DB.
                    compose_run('stop', 'postgres', stdout=log)
                    assert request('/healthz')[0] == 200, 'Database outage killed process liveness'
                    wait_for(lambda: request('/readyz')[0] == 503)
                    compose_run('up', '-d', '--wait', 'postgres', stdout=log)
                    wait_for(lambda: request('/readyz')[0] == 200)
                    assert request(rss_path)[1] == updated and request(atom_path)[1] == updated_atom, 'Database reconnect changed saved feeds'
                    assert request('/api/feeds')[0] == 200, 'Database reconnect restarted the app/session'
                    stop()

                    def database_snapshot(database):
                        result = {}
                        for table in ('schema_version', 'feeds', 'items', 'runs'):
                            query = "SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text), '[]') FROM " + table + " t"
                            rows = compose_run('exec', '-T', 'postgres', 'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', 'rss_workshop', '-d', database, '-Atc', query, stdout=subprocess.PIPE).stdout
                            result[table] = json.loads(rows)
                        return result

                    before_restore = database_snapshot('rss_workshop')
                    dump_path = pathlib.Path(tmp) / 'postgres.dump'
                    with dump_path.open('xb') as dump:
                        dump_path.chmod(0o600)
                        compose_run('exec', '-T', 'postgres', 'pg_dump', '-U', 'rss_workshop', '-d', 'rss_workshop', '--format=custom', stdout=dump)
                    compose_run('exec', '-T', 'postgres', 'createdb', '-U', 'rss_workshop', 'rss_workshop_restored', stdout=log)
                    with dump_path.open('rb') as dump:
                        compose_run('exec', '-T', 'postgres', 'pg_restore', '-U', 'rss_workshop', '-d', 'rss_workshop_restored', '--exit-on-error', '--single-transaction', '--no-owner', '--no-privileges', stdin=dump, stdout=log)
                    assert database_snapshot('rss_workshop_restored') == before_restore, 'PostgreSQL restore changed records, tokens, GUIDs, dates or run history'
                    original_url = urllib.parse.urlsplit(env['DATABASE_URL'])
                    env['DATABASE_URL'] = original_url._replace(path='/rss_workshop_restored').geturl()
                    start()
                    assert request(rss_path)[1] == updated and request(atom_path)[1] == updated_atom, 'Restored PostgreSQL database changed reader output'
                    login()
                    assert set(f['id'] for f in data('/api/feeds')['feeds']) == set(created_ids)
                    state['body'] = fixture.replace(b'First &amp; best', b'Restored story')
                    assert request(f'/api/feeds/{feed_id}/refresh', 'POST', {})[0] == 202
                    wait_for(lambda: b'Restored story' in request(rss_path)[1])
                    assert atom_items(request(atom_path)[1])[1] == guids, 'Restored database could not save a refresh with stable IDs'
                    assert database_snapshot('rss_workshop') == before_restore, 'Restored app wrote to the original database'
                variant = (('browser Compose' if browser_mode else 'static Compose') if compose else 'native')
                backend = 'PostgreSQL' if postgres_mode else 'SQLite'
                print('PASS: ' + variant + ' ' + backend + ' startup, healthcheck, assets, login, preview, save, refresh, independent RSS/Atom parsing, stable IDs, ETags, read isolation, empty retention, restart persistence, recipe export/preview/import, paused copies and fresh tokens.' + (' Database outage/reconnect and pg_dump/restore passed.' if postgres_mode and compose else ''))
            except Exception:
                if compose:
                    subprocess.run(compose + ['logs', '--tail', '20', 'rss-workshop'] + (['postgres'] if postgres_mode else []), env=env)
                raise
            finally:
                cleanup_failed = False
                if postgres_mode and not compose and created_ids and proc is not None and proc.poll() is None:
                    # Only remove IDs returned by this run, never truncate or
                    # reset a database supplied by the developer.
                    try:
                        login()
                        for owned_id in created_ids:
                            assert request(f'/api/feeds/{owned_id}', 'DELETE')[0] == 200
                    except Exception:
                        cleanup_failed = True
                if compose:
                    subprocess.run(compose + ['down', '-v'], env=env, check=True, stdout=log, stderr=log)
                    cleanup_storage()
                if proc is not None and proc.poll() is None:
                    proc.terminate()
                    proc.wait(timeout=10)
                source.shutdown()
                source.server_close()
                if cleanup_failed:
                    raise RuntimeError('Could not remove all native PostgreSQL smoke fixtures; discard the disposable test database before reuse') from None


if __name__ == '__main__':
    main()
