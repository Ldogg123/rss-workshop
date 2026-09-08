#!/usr/bin/env python3
"""Resolve deployment configurations without a daemon or operator settings."""
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parent.parent


class ComposeConfigurationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.docker = shlex.split(os.environ.get('DOCKER', 'docker'))
        if not cls.docker:
            raise ValueError('DOCKER must name a Docker CLI command')

    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='rss-workshop-compose-test-')
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)

    def resolve(self, *overrides, image='', postgres=False):
        # Pass every fixture value through --env-file, including when DOCKER
        # invokes sudo and the child process does not retain environment values.
        values = {
            'ADMIN_PASSWORD': 'synthetic-compose-check-only',
            'PUBLIC_BASE_URL': 'http://localhost:18080',
            'RSS_DATA_DIR': str(self.directory / 'sqlite'),
            'POSTGRES_DATA_DIR': str(self.directory / 'postgres'),
            'POSTGRES_PASSWORD': 'synthetic-postgres-check-only',
            'DATABASE_URL': ('postgres://rss_workshop:synthetic-postgres-check-only'
                             '@postgres:5432/rss_workshop?sslmode=disable') if postgres else '',
            'GLUETUN_CONTAINER': 'synthetic-gluetun-fixture',
            'GLUETUN_APP_PORT': '18080',
        }
        if image is not None:
            values['RSS_IMAGE'] = image
        env_file = self.directory / 'fixture.env'
        env_file.write_text(''.join(f"{key}='{value}'\n" for key, value in values.items()))
        # An allowlist also excludes COMPOSE_FILE, COMPOSE_ENV_FILES and Docker
        # contexts supplied by the operator. No real .env or host storage is read.
        env = {
            'PATH': os.environ.get('PATH', os.defpath),
            'DOCKER_CONFIG': str(self.directory / 'docker-config'),
            'LANG': 'C.UTF-8',
            'COMPOSE_DISABLE_ENV_FILE': '1',
        }
        command = self.docker + ['compose', '--project-directory', str(ROOT),
                                 '--env-file', str(env_file), '--project-name', 'rss-workshop-config-test']
        for name in ('compose.yaml',) + overrides:
            command += ['-f', str(ROOT / name)]
        command += ['config', '--format', 'json']
        result = subprocess.run(command, cwd=self.directory, env=env, text=True,
                                capture_output=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(result.stdout)

    def assert_operator_image(self, config, browser):
        for service in config['services'].values():
            self.assertNotIn('build', service)
            self.assertNotEqual(service.get('pull_policy'), 'build')
        app = config['services']['rss-workshop']
        self.assertEqual(app['image'], 'ghcr.io/ldogg123/rss-workshop:' +
                         ('latest' if browser else 'latest-static'))
        self.assertEqual(app['pull_policy'], 'always')

    def assert_runtime(self, config, browser, postgres=False, gluetun=False):
        app = config['services']['rss-workshop']
        self.assertTrue(app['read_only'])
        self.assertEqual(app['cap_drop'], ['ALL'])
        self.assertIn('no-new-privileges:true', app['security_opt'])
        self.assertEqual(app['environment']['DATA_DIR'], '/data')
        mount = next(volume for volume in app['volumes'] if volume['target'] == '/data')
        self.assertEqual(mount['type'], 'bind')
        self.assertEqual(mount['source'], str(self.directory / 'sqlite'))
        # Some Compose versions omit false-valued fields from resolved JSON.
        self.assertIs(mount['bind'].get('create_host_path', False), False)
        self.assertNotIn('volumes', config)
        seccomp = [value for value in app['security_opt'] if value.startswith('seccomp=')]
        if browser:
            self.assertEqual(app['environment']['CHROMIUM_PATH'], '/usr/bin/chromium')
            self.assertEqual(app['environment']['BROWSER_SLOTS'], '2')
            self.assertEqual(len(seccomp), 1)
            self.assertTrue(seccomp[0].endswith('/deploy/chromium-seccomp.json'))
            self.assertEqual(int(app['shm_size']), 256 << 20)
            self.assertEqual(app['pids_limit'], 256)
            self.assertEqual(int(app['mem_limit']), 2 << 30)
            self.assertEqual(app['tmpfs'], ['/tmp:size=256m,mode=1777'])
        else:
            self.assertEqual(app['environment']['CHROMIUM_PATH'], '')
            self.assertEqual(app['environment']['BROWSER_SLOTS'], '')
            self.assertFalse(seccomp)
            for field in ('shm_size', 'pids_limit', 'mem_limit'):
                self.assertNotIn(field, app)
            self.assertEqual(app['tmpfs'], ['/tmp:size=32m,mode=1777'])
        if gluetun:
            self.assertEqual(app['network_mode'], 'container:synthetic-gluetun-fixture')
            self.assertFalse(app.get('ports'))
            self.assertFalse(app.get('networks'))
            self.assertEqual(app['environment']['LISTEN_ADDR'], ':18080')
        else:
            self.assertNotIn('network_mode', app)
            self.assertEqual(len(app['ports']), 1)
            self.assertEqual(app['ports'][0]['host_ip'], '127.0.0.1')
            self.assertEqual(app['ports'][0]['target'], 8080)
        if postgres:
            self.assertIn('@postgres:5432/rss_workshop?', app['environment']['DATABASE_URL'])
            self.assertEqual(app['depends_on']['postgres']['condition'], 'service_healthy')
            database = config['services']['postgres']
            self.assertTrue(database['image'].startswith('postgres:'))
            self.assertFalse(database.get('ports'))
            mount = next(volume for volume in database['volumes']
                         if volume['target'] == '/var/lib/postgresql')
            self.assertEqual(mount['type'], 'bind')
            self.assertEqual(mount['source'], str(self.directory / 'postgres'))
            self.assertIs(mount['bind'].get('create_host_path', False), False)
        else:
            self.assertEqual(app['environment']['DATABASE_URL'], '')
            self.assertNotIn('postgres', config['services'])

    def test_operator_runtime_database_and_network_combinations_do_not_build(self):
        for browser in (True, False):
            for postgres, gluetun in ((False, False), (True, False), (False, True), (True, True)):
                overrides = [] if browser else ['compose.static.yaml']
                if postgres:
                    overrides.append('compose.postgres.yaml')
                if gluetun:
                    overrides.append('compose.gluetun.yaml')
                with self.subTest(browser=browser, postgres=postgres, gluetun=gluetun):
                    config = self.resolve(*overrides, postgres=postgres)
                    self.assert_operator_image(config, browser)
                    self.assert_runtime(config, browser, postgres, gluetun)

    def test_browser_compatibility_override_selects_browser_without_building(self):
        for preceding in ((), ('compose.static.yaml',)):
            with self.subTest(preceding=preceding):
                config = self.resolve(*preceding, 'compose.browser.yaml')
                self.assert_operator_image(config, True)
                self.assert_runtime(config, True)

    def test_unset_and_blank_image_choose_the_same_published_default(self):
        for overrides in ((), ('compose.static.yaml',)):
            with self.subTest(overrides=overrides):
                self.assertEqual(self.resolve(*overrides, image=None)['services']['rss-workshop']['image'],
                                 self.resolve(*overrides, image='')['services']['rss-workshop']['image'])

    def test_explicit_image_tag_or_digest_is_preserved(self):
        for image in ('ghcr.io/ldogg123/rss-workshop:latest-browser',
                      'ghcr.io/ldogg123/rss-workshop:v0.2.0-browser',
                      'example.invalid/rss-workshop:test-browser',
                      'example.invalid/rss-workshop@sha256:' + 'a' * 64):
            for overrides in ((), ('compose.static.yaml',), ('compose.browser.yaml',)):
                with self.subTest(image=image, overrides=overrides):
                    config = self.resolve(*overrides, image=image)
                    app = config['services']['rss-workshop']
                    self.assertEqual(app['image'], image)
                    self.assertNotIn('build', app)
                    self.assertEqual(app['pull_policy'], 'always')

    def test_only_explicit_development_overrides_enable_local_builds(self):
        for browser in (True, False):
            overrides = ('compose.build.yaml',) if browser else ('compose.static.yaml', 'compose.build.static.yaml')
            with self.subTest(browser=browser):
                config = self.resolve(*overrides, image='example.invalid/ignored:fixture')
                app = config['services']['rss-workshop']
                self.assertEqual(app['image'], 'rss-workshop:local' if browser else 'rss-workshop:local-static')
                self.assertEqual(app['pull_policy'], 'build')
                self.assertEqual(app['build']['context'], str(ROOT))
                self.assertEqual(app['build']['dockerfile'], 'Dockerfile.browser' if browser else 'Dockerfile')
                self.assertEqual(app['build'].get('target'), 'app' if browser else None)
                self.assert_runtime(config, browser)

    def test_legacy_image_override_removes_local_build_and_its_pull_policy(self):
        image = 'example.invalid/rss-workshop@sha256:' + 'b' * 64
        for browser in (True, False):
            overrides = ('compose.build.yaml',) if browser else ('compose.static.yaml', 'compose.build.static.yaml')
            with self.subTest(browser=browser):
                config = self.resolve(*overrides, 'compose.image.yaml', image=image)
                app = config['services']['rss-workshop']
                self.assertEqual(app['image'], image)
                self.assertNotIn('build', app)
                self.assertNotIn('pull_policy', app)
                self.assert_runtime(config, browser)

    def test_operator_environment_cannot_override_fixture_values(self):
        with mock.patch.dict(os.environ, {
                'COMPOSE_FILE': '/operator/private-compose.yaml',
                'COMPOSE_ENV_FILES': '/operator/private.env',
                'RSS_IMAGE': 'operator/private:wrong',
                'DATABASE_URL': 'postgres://operator/private',
                'RSS_DATA_DIR': '/operator/sqlite',
                'POSTGRES_DATA_DIR': '/operator/postgres',
                'CHROMIUM_PATH': '/operator/chromium',
                'BROWSER_SLOTS': '99',
                'GLUETUN_CONTAINER': 'operator-vpn',
                'PUBLIC_BASE_URL': 'https://operator.invalid',
        }):
            config = self.resolve()
        self.assert_operator_image(config, True)
        self.assert_runtime(config, True)
        self.assertEqual(config['services']['rss-workshop']['environment']['PUBLIC_BASE_URL'],
                         'http://localhost:18080')


if __name__ == '__main__':
    unittest.main()
