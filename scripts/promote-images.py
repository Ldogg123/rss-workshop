#!/usr/bin/env python3
"""Promote published stable image indexes without rebuilding or retagging releases.

Dry-run by default. GitHub Actions serializes executions of this helper through
promote-images.yml; all three aliases are checked before the first write.
"""

import argparse
import json
import os
import re
import shlex
import subprocess
from pathlib import Path


DIGEST = re.compile(r'sha256:[a-f0-9]{64}\Z')
COMMIT = re.compile(r'[a-f0-9]{40}\Z')
VERSION = re.compile(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\Z')
INDEX_TYPES = {'application/vnd.oci.image.index.v1+json',
               'application/vnd.docker.distribution.manifest.list.v2+json'}
IMAGE_TYPES = {'application/vnd.oci.image.manifest.v1+json',
               'application/vnd.docker.distribution.manifest.v2+json'}
# Publish the default last. Registries cannot atomically update several tags.
ALIASES = [('latest-static', 'static'), ('latest-browser', 'browser'), ('latest', 'browser')]


def require(condition, message):
    if not condition:
        raise ValueError(message)


def stable_version(value):
    match = VERSION.fullmatch(value) if isinstance(value, str) and len(value) <= 100 else None
    require(match is not None, 'Expected a stable vMAJOR.MINOR.PATCH version.')
    return tuple(int(part) for part in match.groups())


class Publisher:
    def __init__(self, repository, docker):
        require(re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9][A-Za-z0-9_.-]*', repository),
                'Expected a GitHub owner/repository.')
        require(bool(docker), 'Docker command is empty.')
        self.repository = repository
        self.image = 'ghcr.io/' + repository.lower()
        self.docker = docker

    def command(self, args, missing=False):
        result = subprocess.run(args, capture_output=True, text=True, timeout=120)
        if result.returncode:
            # Authentication, rate limits and other registry errors must not be
            # treated as an absent alias. Do not print arbitrary remote output.
            if missing and re.search(r': not found\b|manifest unknown|MANIFEST_UNKNOWN', result.stderr):
                return None
            raise ValueError(f'{args[0]} command failed; no further aliases were updated.')
        require(len(result.stdout) <= 8 << 20, 'Command metadata exceeded its size limit.')
        return result.stdout

    def api(self, endpoint):
        return json.loads(self.command(['gh', 'api', f'repos/{self.repository}/{endpoint}']))

    def release(self, version):
        candidate = stable_version(version)
        release = self.api('releases/tags/' + version)
        require(release.get('tag_name') == version and release.get('draft') is False
                and release.get('prerelease') is False, 'Expected a published stable release.')
        require(type(release.get('id')) is int and release['id'] > 0, 'Invalid release identity.')
        newest = candidate
        for page in range(1, 101):
            releases = self.api(f'releases?per_page=100&page={page}')
            require(isinstance(releases, list), 'Invalid GitHub release list.')
            for other in releases:
                if other.get('draft') is not False or other.get('prerelease') is not False:
                    continue
                tag = other.get('tag_name')
                if isinstance(tag, str) and len(tag) <= 100 and VERSION.fullmatch(tag):
                    newest = max(newest, stable_version(tag))
            if len(releases) < 100:
                break
        else:
            raise ValueError('Release list exceeded 10,000 entries; review pagination before promotion.')
        obj = self.api('git/ref/tags/' + version)['object']
        for _ in range(8):
            require(COMMIT.fullmatch(obj.get('sha', '')), 'Invalid Git tag object.')
            if obj.get('type') == 'commit':
                return {'id': release['id'], 'commit': obj['sha'], 'newest': newest == candidate}
            require(obj.get('type') == 'tag', 'Release tag does not resolve to a commit.')
            obj = self.api('git/tags/' + obj['sha'])['object']
        raise ValueError('Release tag nesting exceeds the supported limit.')

    def descriptor(self, reference, missing=False):
        raw = self.command(self.docker + ['buildx', 'imagetools', 'inspect', reference,
                                          '--format', '{{json .Manifest}}'], missing=missing)
        if raw is None:
            return None
        descriptor = json.loads(raw)
        require(descriptor.get('schemaVersion') == 2 and descriptor.get('mediaType') in INDEX_TYPES
                and DIGEST.fullmatch(descriptor.get('digest', '')), 'Expected a multi-platform image index.')
        return descriptor

    def inspect(self, reference, variant, version=None, commit=None, missing=False):
        index = self.descriptor(reference, missing=missing)
        if index is None:
            return None
        manifests = index.get('manifests')
        require(isinstance(manifests, list) and len(manifests) == 2, 'Expected both release architectures.')
        seen, identities = set(), set()
        for manifest in manifests:
            platform = manifest.get('platform', {})
            arch = platform.get('architecture')
            require(platform.get('os') == 'linux' and arch in ('amd64', 'arm64') and arch not in seen,
                    'Unexpected or duplicate image architecture.')
            require(manifest.get('mediaType') in IMAGE_TYPES and DIGEST.fullmatch(manifest.get('digest', '')),
                    'Invalid platform image descriptor.')
            seen.add(arch)
            immutable = self.image + '@' + manifest['digest']
            config = json.loads(self.command(self.docker + ['buildx', 'imagetools', 'inspect', immutable,
                                                            '--format', '{{json .Image}}']))
            require(config.get('architecture') == arch and config.get('os') == 'linux', 'Image platform mismatch.')
            runtime = config.get('config', {})
            labels = runtime.get('Labels', {})
            image_version = labels.get('org.opencontainers.image.version')
            image_commit = labels.get('org.opencontainers.image.revision', '')
            stable_version(image_version)
            require(COMMIT.fullmatch(image_commit), 'Invalid image revision label.')
            require(version is None or version == image_version, 'Image release version mismatch.')
            require(commit is None or commit == image_commit, 'Image release commit mismatch.')
            require(labels.get('org.opencontainers.image.source') == 'https://github.com/' + self.repository
                    and labels.get('org.opencontainers.image.licenses') == 'MIT', 'Image source/license mismatch.')
            require(runtime.get('User') == '65532:65532' and runtime.get('Entrypoint') == ['/rss-workshop'],
                    'Unexpected image runtime identity.')
            require(('CHROMIUM_PATH=/usr/bin/chromium' in runtime.get('Env', [])) == (variant == 'browser'),
                    'Image rendering variant mismatch.')
            identities.add((image_version, image_commit))
        require(len(identities) == 1, 'Image architectures contain different releases.')
        image_version, image_commit = identities.pop()
        return {'digest': index['digest'], 'version': image_version, 'commit': image_commit}

    def promote(self, version, execute=False):
        target = stable_version(version)
        release = self.release(version)
        if not release['newest']:
            return {'status': 'skipped', 'reason': 'A newer stable GitHub release exists.', 'version': version}
        sources = {variant: self.inspect(f'{self.image}:{version}-{variant}', variant, version, release['commit'])
                   for variant in ('static', 'browser')}
        previous = {alias: self.inspect(f'{self.image}:{alias}', variant, missing=True) for alias, variant in ALIASES}
        for alias, variant in ALIASES:
            current = previous[alias]
            if current and stable_version(current['version']) > target:
                return {'status': 'skipped', 'reason': 'An alias already points to a newer release.', 'version': version}
            require(not current or current['version'] != version or current['digest'] == sources[variant]['digest'],
                    'An alias and immutable version tag disagree for the same release.')
        result = {'status': 'planned', 'version': version, 'commit': release['commit'],
                  'sources': sources, 'previous': previous, 'updated': []}
        if not execute:
            return result

        # Freeze both source indexes and preflight every destination before any
        # registry write. Repeat release and alias checks immediately before each
        # write to detect external changes in addition to the Actions lock.
        for alias, variant in ALIASES:
            require(self.release(version) == release, 'Release selection changed; rerun promotion for the newest stable release.')
            source = sources[variant]['digest']
            require(self.descriptor(f'{self.image}:{version}-{variant}')['digest'] == source,
                    'An immutable version tag changed during promotion.')
            current = self.descriptor(f'{self.image}:{alias}', missing=True)
            before = previous[alias]
            require((current['digest'] if current else None) == (before['digest'] if before else None),
                    'An alias changed outside this promotion; inspect and rerun.')
            if before and before['digest'] == source:
                continue
            self.command(self.docker + ['buildx', 'imagetools', 'create', '--tag', f'{self.image}:{alias}',
                                        self.image + '@' + source])
            require(self.descriptor(f'{self.image}:{alias}')['digest'] == source, 'Promoted alias digest mismatch.')
            result['updated'].append(alias)
            print(f'Verified {self.image}:{alias} -> {source}', flush=True)
        for alias, variant in ALIASES:
            require(self.descriptor(f'{self.image}:{alias}')['digest'] == sources[variant]['digest'],
                    'An alias changed before promotion finished; inspect and rerun.')
        result['status'] = 'promoted'
        return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--repository', default=os.environ.get('GITHUB_REPOSITORY', 'Ldogg123/rss-workshop'))
    parser.add_argument('--version', required=True)
    parser.add_argument('--docker', default='docker')
    parser.add_argument('--execute', action='store_true')
    parser.add_argument('--report', type=Path)
    args = parser.parse_args()
    publisher = Publisher(args.repository, shlex.split(args.docker))
    result = publisher.promote(args.version, args.execute)
    encoded = json.dumps(result, indent=2) + '\n'
    if args.report:
        args.report.write_text(encoded)
    print(encoded, end='')


if __name__ == '__main__':
    try:
        main()
    except (ValueError, KeyError, TypeError, AttributeError, OSError, subprocess.SubprocessError) as error:
        raise SystemExit(str(error))
