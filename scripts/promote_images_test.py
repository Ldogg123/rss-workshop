#!/usr/bin/env python3
"""Offline mutation-boundary checks for stable image promotion."""
import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('promotion', Path(__file__).with_name('promote-images.py'))
promotion = importlib.util.module_from_spec(spec)
spec.loader.exec_module(promotion)


def digest(text):
    return 'sha256:' + hashlib.sha256(text.encode()).hexdigest()


class Fixture(promotion.Publisher):
    def __init__(self):
        super().__init__('Ldogg123/rss-workshop', ['docker'])
        self.releases, self.tags, self.indexes, self.configs, self.writes = [], {}, {}, {}, []
        self.fail_alias = None
        self.add_release('v0.2.0')

    def add_release(self, version, prerelease=False, draft=False):
        revision = hashlib.sha1(version.encode()).hexdigest()
        self.releases.append({'id': len(self.releases) + 1, 'tag_name': version,
                              'draft': draft, 'prerelease': prerelease})
        self.tags[version] = {'type': 'commit', 'sha': revision}
        for variant in ('static', 'browser'):
            descriptors = []
            for arch in ('amd64', 'arm64'):
                child = digest(version + variant + arch)
                descriptors.append({'mediaType': 'application/vnd.oci.image.manifest.v1+json',
                                    'digest': child, 'platform': {'architecture': arch, 'os': 'linux'}})
                self.configs[self.image + '@' + child] = {
                    'architecture': arch, 'os': 'linux', 'config': {
                        'User': '65532:65532', 'Entrypoint': ['/rss-workshop'],
                        'Env': ['CHROMIUM_PATH=/usr/bin/chromium'] if variant == 'browser' else [],
                        'Labels': {'org.opencontainers.image.version': version,
                                   'org.opencontainers.image.revision': revision,
                                   'org.opencontainers.image.source': 'https://github.com/' + self.repository,
                                   'org.opencontainers.image.licenses': 'MIT'}}}
            index = {'schemaVersion': 2, 'mediaType': 'application/vnd.oci.image.index.v1+json',
                     'digest': digest(version + variant), 'manifests': descriptors}
            self.indexes[f'{self.image}:{version}-{variant}'] = index
            self.indexes[self.image + '@' + index['digest']] = index

    def alias(self, alias, version, variant):
        self.indexes[self.image + ':' + alias] = copy.deepcopy(self.indexes[f'{self.image}:{version}-{variant}'])

    def api(self, endpoint):
        if endpoint.startswith('releases/tags/'):
            return copy.deepcopy(next(r for r in self.releases if r['tag_name'] == endpoint.rsplit('/', 1)[1]))
        if endpoint.startswith('releases?'):
            return copy.deepcopy(self.releases)
        if endpoint.startswith('git/ref/tags/'):
            return {'object': copy.deepcopy(self.tags[endpoint.rsplit('/', 1)[1]])}
        if endpoint.startswith('git/tags/'):
            return {'object': {'type': 'commit', 'sha': hashlib.sha1(b'v0.2.0').hexdigest()}}
        raise AssertionError(endpoint)

    def command(self, args, missing=False):
        if args[3] == 'inspect':
            reference = args[4]
            if args[-1] == '{{json .Image}}':
                return json.dumps(self.configs[reference])
            if reference not in self.indexes:
                if missing:
                    return None
                raise ValueError('Missing source manifest')
            return json.dumps(self.indexes[reference])
        self.assert_create(args)
        alias = args[5].rsplit(':', 1)[1]
        if alias == self.fail_alias:
            raise ValueError('fixture upload failure')
        self.writes.append((args[5], args[6]))
        self.indexes[args[5]] = copy.deepcopy(self.indexes[args[6]])
        return ''

    def assert_create(self, args):
        assert args[:5] == ['docker', 'buildx', 'imagetools', 'create', '--tag']
        assert args[5] in {self.image + ':' + alias for alias, _ in promotion.ALIASES}
        assert args[6].startswith(self.image + '@sha256:')
        assert len(args) == 7


class PromotionTests(unittest.TestCase):
    def test_dry_run_has_no_registry_writes(self):
        p = Fixture()
        result = p.promote('v0.2.0')
        self.assertEqual(result['status'], 'planned')
        self.assertEqual(p.writes, [])

    def test_initial_promotion_and_idempotent_retry_keep_version_tags(self):
        p = Fixture()
        originals = copy.deepcopy(p.indexes)
        self.assertEqual(p.promote('v0.2.0', True)['updated'], ['latest-static', 'latest-browser', 'latest'])
        self.assertEqual(p.promote('v0.2.0', True)['updated'], [])
        self.assertEqual(len(p.writes), 3)
        for reference, index in originals.items():
            self.assertEqual(p.indexes[reference], index)

    def test_partial_promotion_is_repairable(self):
        p = Fixture()
        p.fail_alias = 'latest-browser'
        with self.assertRaisesRegex(ValueError, 'fixture upload failure'):
            p.promote('v0.2.0', True)
        self.assertEqual(len(p.writes), 1)
        self.assertNotIn(p.image + ':latest', p.indexes)
        p.fail_alias = None
        self.assertEqual(p.promote('v0.2.0', True)['updated'], ['latest-browser', 'latest'])

    def test_newer_published_stable_version_skips_before_writes(self):
        p = Fixture()
        p.add_release('v0.9.0')
        p.add_release('v0.10.0')
        self.assertEqual(p.promote('v0.9.0', True)['status'], 'skipped')
        self.assertEqual(p.writes, [])
        self.assertEqual(p.promote('v0.10.0', True)['status'], 'promoted')

    def test_drafts_and_prereleases_do_not_supersede_stable(self):
        p = Fixture()
        p.add_release('v0.3.0', draft=True)
        p.add_release('v0.4.0', prerelease=True)
        self.assertEqual(p.promote('v0.2.0', True)['status'], 'promoted')

    def test_prerelease_and_unpublished_candidates_rejected(self):
        for version, flags in [('v0.3.0-rc.1', {}), ('v0.3.0', {'prerelease': True}),
                               ('v0.3.0', {'draft': True})]:
            with self.subTest(version=version, flags=flags):
                p = Fixture()
                p.add_release(version, **flags)
                with self.assertRaises(ValueError):
                    p.promote(version, True)
                self.assertEqual(p.writes, [])

    def test_newer_alias_prevents_all_writes_even_if_release_was_removed(self):
        p = Fixture()
        p.add_release('v0.10.0')
        p.alias('latest', 'v0.10.0', 'browser')
        p.releases.pop()
        self.assertEqual(p.promote('v0.2.0', True)['status'], 'skipped')
        self.assertEqual(p.writes, [])

    def test_same_version_with_different_digest_is_rejected(self):
        p = Fixture()
        p.alias('latest', 'v0.2.0', 'browser')
        p.indexes[p.image + ':latest']['digest'] = digest('replaced')
        with self.assertRaisesRegex(ValueError, 'disagree'):
            p.promote('v0.2.0', True)
        self.assertEqual(p.writes, [])

    def test_missing_variant_or_architecture_does_not_publish_partial_defaults(self):
        for mutation in ('variant', 'architecture', 'duplicate'):
            p = Fixture()
            key = p.image + ':v0.2.0-browser'
            if mutation == 'variant':
                del p.indexes[key]
            elif mutation == 'architecture':
                p.indexes[key]['manifests'].pop()
            else:
                p.indexes[key]['manifests'][1] = p.indexes[key]['manifests'][0]
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                p.promote('v0.2.0', True)
            self.assertEqual(p.writes, [])

    def test_mismatched_image_identity_is_rejected_before_any_alias_write(self):
        changes = [('version', 'v0.1.2'), ('revision', 'a' * 40), ('source', 'https://example.com'), ('licenses', 'wrong')]
        for suffix, value in changes:
            p = Fixture()
            next(iter(p.configs.values()))['config']['Labels']['org.opencontainers.image.' + suffix] = value
            with self.subTest(suffix=suffix), self.assertRaises(ValueError):
                p.promote('v0.2.0', True)
            self.assertEqual(p.writes, [])

    def test_annotated_tag_is_resolved(self):
        p = Fixture()
        p.tags['v0.2.0'] = {'type': 'tag', 'sha': 'f' * 40}
        self.assertEqual(p.promote('v0.2.0')['status'], 'planned')

    def test_release_change_between_plan_and_execution_prevents_writes(self):
        p = Fixture()
        original = p.release
        calls = 0
        def release(version):
            nonlocal calls
            calls += 1
            result = original(version)
            if calls > 1:
                result['commit'] = 'a' * 40
            return result
        p.release = release
        with self.assertRaisesRegex(ValueError, 'Release selection changed'):
            p.promote('v0.2.0', True)
        self.assertEqual(p.writes, [])

    def test_alias_changed_after_preflight_is_not_overwritten(self):
        p = Fixture()
        original = p.descriptor
        calls = 0
        def descriptor(reference, missing=False):
            nonlocal calls
            if reference.endswith(':latest-static'):
                calls += 1
                if calls == 2:
                    p.alias('latest-static', 'v0.2.0', 'static')
            return original(reference, missing)
        p.descriptor = descriptor
        with self.assertRaisesRegex(ValueError, 'outside this promotion'):
            p.promote('v0.2.0', True)
        self.assertEqual(p.writes, [])

    def test_source_tag_changed_after_preflight_prevents_writes(self):
        p = Fixture()
        original = p.descriptor
        calls = 0
        def descriptor(reference, missing=False):
            nonlocal calls
            if reference.endswith(':v0.2.0-static'):
                calls += 1
                if calls == 2:
                    p.indexes[reference]['digest'] = digest('external replacement')
            return original(reference, missing)
        p.descriptor = descriptor
        with self.assertRaisesRegex(ValueError, 'immutable version tag changed'):
            p.promote('v0.2.0', True)
        self.assertEqual(p.writes, [])

    def test_external_change_to_earlier_alias_prevents_success_report(self):
        for already_current in (False, True):
            with self.subTest(already_current=already_current):
                p = Fixture()
                p.add_release('v0.1.2')
                if already_current:
                    p.alias('latest-static', 'v0.2.0', 'static')
                original = p.command
                def command(args, missing=False):
                    result = original(args, missing)
                    if args[3] == 'create' and args[5] == p.image + ':latest':
                        p.alias('latest-static', 'v0.1.2', 'static')
                    return result
                p.command = command
                with self.assertRaisesRegex(ValueError, 'before promotion finished'):
                    p.promote('v0.2.0', True)
                self.assertEqual(len(p.writes), 2 if already_current else 3)
                self.assertEqual(p.indexes[p.image + ':latest-static'],
                                 p.indexes[p.image + ':v0.1.2-static'])

    def test_registry_errors_are_not_treated_as_missing(self):
        p = promotion.Publisher('Ldogg123/rss-workshop', ['docker'])
        for error in ('unauthorized: authentication required', '429 Too Many Requests', 'connection refused'):
            result = subprocess.CompletedProcess([], 1, '', error)
            with mock.patch.object(promotion.subprocess, 'run', return_value=result), self.assertRaises(ValueError):
                p.descriptor(p.image + ':latest', missing=True)
        result = subprocess.CompletedProcess([], 1, '', 'ghcr.io/example:latest: not found')
        with mock.patch.object(promotion.subprocess, 'run', return_value=result):
            self.assertIsNone(p.descriptor(p.image + ':latest', missing=True))


if __name__ == '__main__':
    unittest.main()
