#!/usr/bin/env python3
"""Offline checks for the dependency table and notice verifier."""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('dependencies', Path(__file__).with_name('check-dependencies.py'))
dependencies = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dependencies)
Module = dependencies.Module

TABLE = '''# Dependency provenance

Prose before the table.

| Module | Pinned version | Copied license notice | Runtime |
| --- | --- | --- | --- |
| example.com/linked | v1.2.0 | [LICENSE-3RD-PARTY.md](licenses/example.com_linked@v1.2.0_LICENSE-3RD-PARTY.md), [LICENSE](licenses/example.com_linked@v1.2.0_LICENSE) | Yes |
| example.com/other/v2 | v2.0.1 | [LICENSE.md](licenses/example.com_other_v2@v2.0.1_LICENSE.md) | Yes |
| example.com/tool | v0.3.0 | [LICENSE](licenses/example.com_tool@v0.3.0_LICENSE) | No |

Prose after the table.
'''
LICENSING = '''# Licensing

Generated declarations keep notices in `example.com_linked@v1.2.0_GENERATED-HEADER-NOTICES`, alongside
their license texts. The module `example.com/linked@v1.2.0` is described here too.
'''
README = 'example.com_linked@v1.2.0_GENERATED-HEADER-NOTICES preserves generated notices.\n'
UPSTREAM = {
    'example.com/linked@v1.2.0': {'LICENSE': b'linked license\n', 'LICENSE-3RD-PARTY.md': b'third parties\n',
                                  'AUTHORS': b'people\n', 'PATENTS': b'grant\n', 'license.go': b'package linked\n'},
    'example.com/other/v2@v2.0.1': {'LICENSE.md': b'other license\n'},
    # Never compared: the module is not linked.
    'example.com/tool@v0.3.0': {'LICENSE': b'tool license, newer wording\n'},
}


class Fixture:
    def __init__(self, directory):
        self.root = Path(directory) / 'repo'
        self.cache = Path(directory) / 'cache'
        licenses = self.root / 'docs/licenses'
        licenses.mkdir(parents=True)
        self.write('docs/dependencies.md', TABLE)
        self.write('docs/licensing.md', LICENSING)
        self.write('docs/licenses/README.md', README)
        for name, data in {
            'example.com_linked@v1.2.0_LICENSE': b'linked license\n',
            'example.com_linked@v1.2.0_LICENSE-3RD-PARTY.md': b'third parties\n',
            'example.com_linked@v1.2.0_GENERATED-HEADER-NOTICES': b'compiled from generated headers\n',
            'example.com_other_v2@v2.0.1_LICENSE.md': b'other license\n',
            'example.com_tool@v0.3.0_LICENSE': b'tool license\n',
            'Go-LICENSE': b'not a module notice\n',
        }.items():
            (licenses / name).write_bytes(data)
        for module, files in UPSTREAM.items():
            directory = self.cache / module
            directory.mkdir(parents=True)
            for name, data in files.items():
                (directory / name).write_bytes(data)
        self.runtime = {
            'example.com/linked': Module('v1.2.0', str(self.cache / 'example.com/linked@v1.2.0'), arches={'amd64', 'arm64'}),
            'example.com/other/v2': Module('v2.0.1', str(self.cache / 'example.com/other/v2@v2.0.1'), arches={'arm64'}),
        }
        self.graph = {
            'example.com/linked': Module('v1.2.0', self.runtime['example.com/linked'].directory),
            'example.com/other/v2': Module('v2.0.1', self.runtime['example.com/other/v2'].directory),
            'example.com/tool': Module('v0.3.0', str(self.cache / 'example.com/tool@v0.3.0')),
            # Graph modules that only tests require need no row.
            'example.com/testonly': Module('v1.0.0'),
        }

    def write(self, name, text):
        (self.root / name).write_text(text, encoding='utf-8')

    def edit(self, name, old, new):
        path = self.root / name
        text = path.read_text(encoding='utf-8')
        assert old in text, old
        path.write_text(text.replace(old, new), encoding='utf-8')

    def check(self):
        return dependencies.check(self.root, self.runtime, self.graph)


class CheckTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.fixture = Fixture(directory.name)

    def assertProblem(self, text):
        problems, _, _ = self.fixture.check()
        self.assertTrue(any(text in problem for problem in problems), f'{text!r} not in {problems}')

    def test_matching_tree_passes_and_compares_linked_notices(self):
        problems, notes, compared = self.fixture.check()
        self.assertEqual(problems, [])
        self.assertEqual(notes, [])
        self.assertEqual(compared, 3)

    def test_linked_module_without_a_row(self):
        self.fixture.runtime['example.com/new'] = Module('v0.1.0', arches={'amd64'})
        self.fixture.graph['example.com/new'] = Module('v0.1.0')
        self.assertProblem('example.com/new v0.1.0 is linked on linux/amd64 but has no row')

    def test_linked_module_marked_no(self):
        self.fixture.runtime['example.com/tool'] = Module('v0.3.0', arches={'amd64', 'arm64'})
        self.assertProblem('example.com/tool is linked on linux/amd64 and linux/arm64, so its Runtime must be Yes')

    def test_runtime_row_that_is_no_longer_linked(self):
        del self.fixture.runtime['example.com/other/v2']
        self.assertProblem('example.com/other/v2 is marked Runtime Yes, but neither')

    def test_row_for_a_module_that_left_the_graph(self):
        del self.fixture.graph['example.com/tool']
        self.assertProblem('example.com/tool is not in the module graph')

    def test_pinned_version_differs_from_the_graph(self):
        self.fixture.graph['example.com/tool'] = Module('v0.4.0')
        self.assertProblem('example.com/tool is pinned at v0.3.0, but the module graph selects v0.4.0')
        self.fixture.runtime['example.com/linked'].version = 'v1.3.0'
        self.fixture.graph['example.com/linked'] = Module('v1.3.0')
        problems, _, compared = self.fixture.check()
        self.assertTrue(any('example.com/linked is pinned at v1.2.0' in problem for problem in problems), problems)
        # An outdated row is not compared with the newer module, which would
        # only add misleading instructions to copy its notices.
        self.assertEqual(compared, 1)
        self.assertEqual([problem for problem in problems if 'example.com/linked' in problem],
                         ['docs/dependencies.md:7: example.com/linked is pinned at v1.2.0, but the module graph selects v1.3.0.'])

    def test_runtime_row_without_a_notice(self):
        self.fixture.edit('docs/dependencies.md', '[LICENSE.md](licenses/example.com_other_v2@v2.0.1_LICENSE.md)', '')
        self.assertProblem('example.com/other/v2 needs its notices as comma-separated links')

    def test_linked_notice_is_missing(self):
        (self.fixture.root / 'docs/licenses/example.com_other_v2@v2.0.1_LICENSE.md').unlink()
        self.assertProblem('links licenses/example.com_other_v2@v2.0.1_LICENSE.md, which does not exist')

    def test_row_links_the_notice_of_another_version(self):
        self.fixture.edit('docs/dependencies.md', '| example.com/tool | v0.3.0 |', '| example.com/tool | v0.4.0 |')
        self.fixture.graph['example.com/tool'] = Module('v0.4.0')
        self.assertProblem('links licenses/example.com_tool@v0.3.0_LICENSE, which is not named example.com_tool@v0.4.0_')
        # The copy itself is reported as outdated too.
        self.assertProblem('docs/licenses/example.com_tool@v0.3.0_LICENSE is for v0.3.0, but docs/dependencies.md pins')

    def test_unreferenced_notice(self):
        (self.fixture.root / 'docs/licenses/example.com_tool@v0.3.0_COPYING').write_bytes(b'unlisted\n')
        self.assertProblem('docs/licenses/example.com_tool@v0.3.0_COPYING is neither linked')

    def test_notice_named_only_by_the_licensing_guide_is_accepted(self):
        self.fixture.write('docs/licensing.md', '# Licensing\n')
        self.fixture.write('docs/licenses/README.md', '')
        self.assertProblem('example.com_linked@v1.2.0_GENERATED-HEADER-NOTICES is neither linked')

    def test_licensing_guide_may_link_to_a_notice_or_give_its_path(self):
        name = 'example.com_linked@v1.2.0_GENERATED-HEADER-NOTICES'
        for reference in (f'[the generated-header notices](licenses/{name})', f'`docs/licenses/{name}`'):
            self.fixture.write('docs/licensing.md', f'# Licensing\n\nSee {reference}.\n')
            problems, _, _ = self.fixture.check()
            self.assertEqual(problems, [], reference)
        old = name.replace('v1.2.0', 'v1.1.0')
        self.fixture.write('docs/licensing.md', f'# Licensing\n\nSee [the notices](licenses/{name}) '
                                                f'and [the old ones](licenses/{old}).\n')
        self.assertProblem(f'docs/licensing.md names {old}, which docs/licenses/ does not contain')

    def test_guide_names_a_missing_notice(self):
        self.fixture.edit('docs/licensing.md', 'v1.2.0_GENERATED', 'v1.1.0_GENERATED')
        self.assertProblem('docs/licensing.md names example.com_linked@v1.1.0_GENERATED-HEADER-NOTICES, which docs/licenses/ does not contain')
        self.fixture.edit('docs/licensing.md', 'v1.1.0_GENERATED', 'v1.2.0_GENERATED')
        self.fixture.write('docs/licenses/README.md', README.replace('v1.2.0', 'v1.1.0'))
        self.assertProblem('docs/licenses/README.md names example.com_linked@v1.1.0_GENERATED-HEADER-NOTICES')

    def test_notice_named_by_the_guide_follows_the_pinned_version(self):
        licenses = self.fixture.root / 'docs/licenses'
        (licenses / 'example.com_linked@v1.2.0_GENERATED-HEADER-NOTICES').rename(
            licenses / 'example.com_linked@v1.1.0_GENERATED-HEADER-NOTICES')
        for name in ('docs/licensing.md', 'docs/licenses/README.md'):
            self.fixture.edit(name, 'v1.2.0_GENERATED', 'v1.1.0_GENERATED')
        self.assertProblem('example.com_linked@v1.1.0_GENERATED-HEADER-NOTICES is for v1.1.0, but docs/dependencies.md pins example.com/linked at v1.2.0')

    def test_notice_for_a_module_without_a_row(self):
        (self.fixture.root / 'docs/licenses/example.com_gone@v1.0.0_LICENSE').write_bytes(b'gone\n')
        self.fixture.write('docs/licensing.md', LICENSING + 'Keep `example.com_gone@v1.0.0_LICENSE`.\n')
        self.assertProblem('docs/licenses/example.com_gone@v1.0.0_LICENSE belongs to no module')

    def test_runtime_notice_differs_from_upstream(self):
        (self.fixture.cache / 'example.com/linked@v1.2.0/LICENSE').write_bytes(b'linked license, revised\n')
        self.assertProblem('docs/licenses/example.com_linked@v1.2.0_LICENSE differs from LICENSE in example.com/linked v1.2.0')

    def test_runtime_notice_that_upstream_no_longer_ships(self):
        (self.fixture.cache / 'example.com/other/v2@v2.0.1/LICENSE.md').rename(
            self.fixture.cache / 'example.com/other/v2@v2.0.1/LICENSE')
        self.assertProblem('example.com_other_v2@v2.0.1_LICENSE.md: example.com/other/v2 v2.0.1 has no LICENSE.md')
        self.assertProblem('example.com/other/v2 v2.0.1 ships LICENSE, which its row does not link')

    def test_new_upstream_notice_needs_a_copy(self):
        # The fixture's AUTHORS, PATENTS and license.go need no copy, as the
        # matching tree shows; any other name starting with a notice word does.
        for name in ('LICENSE-SQLITE', 'NOTICES', 'LICENSE2', 'COPYINGv3', 'licence.txt'):
            (self.fixture.cache / 'example.com/linked@v1.2.0' / name).write_bytes(b'notice\n')
            self.assertProblem(f'example.com/linked v1.2.0 ships {name}, which its row does not link; '
                               f'copy it to docs/licenses/example.com_linked@v1.2.0_{name}')

    def test_module_missing_from_the_cache_is_reported_without_failing(self):
        self.fixture.runtime['example.com/other/v2'].directory = ''
        self.fixture.runtime['example.com/linked'].directory = str(self.fixture.cache / 'absent')
        problems, notes, compared = self.fixture.check()
        self.assertEqual(problems, [])
        self.assertEqual(compared, 0)
        self.assertEqual(len(notes), 2)
        self.assertIn('example.com/other/v2 v2.0.1 is not in the module cache', notes[1])

    def test_replaced_runtime_module_is_refused(self):
        self.fixture.runtime['example.com/linked'].replacement = 'example.com/fork v1.2.1'
        problems, _, compared = self.fixture.check()
        self.assertTrue(any('example.com/linked is replaced by example.com/fork v1.2.1' in problem
                            for problem in problems), problems)
        self.assertEqual(compared, 1)

    def test_table_format_problems(self):
        self.fixture.edit('docs/dependencies.md', 'v0.3.0_LICENSE) | No |', 'v0.3.0_LICENSE) | Maybe |')
        self.assertProblem("example.com/tool needs Runtime Yes or No, not 'Maybe'")
        self.fixture.edit('docs/dependencies.md', '| example.com/tool | v0.3.0 |', '| example.com/linked | v1.2.0 |')
        self.assertProblem('example.com/linked has a second row')
        self.fixture.edit('docs/dependencies.md', '| Maybe |', '| No | extra |')
        self.assertProblem('expected 4 cells, found 5')
        self.fixture.edit('docs/dependencies.md', '| example.com/other/v2 | v2.0.1 | [LICENSE.md]',
                          '| example.com/other/v2 | v2.0.1 | see upstream, [LICENSE.md]')
        self.assertProblem('example.com/other/v2 needs its notices as comma-separated links')
        self.fixture.edit('docs/dependencies.md', '| Copied license notice |', '| Notice |')
        with self.assertRaisesRegex(ValueError, 'has no .* table'):
            self.fixture.check()


FAKE_GO = r'''
import json, os, sys
args = sys.argv[1:]
with open(os.environ['FAKE_GO_LOG'], 'a') as log:
    log.write(json.dumps({'args': args, 'cwd': os.getcwd(), 'env': {key: os.environ.get(key) for key in
                          ('CGO_ENABLED', 'GOOS', 'GOARCH', 'GOWORK', 'GOFLAGS')}}) + '\n')
outputs = json.loads(os.environ['FAKE_GO_OUTPUTS'])
if os.environ.get('FAKE_GO_FAIL'):
    sys.stderr.write('go: updates to go.sum needed, disabled by -mod=readonly\n')
    sys.exit(1)
key = os.environ['GOARCH'] if '-deps' in args else 'graph'
sys.stdout.write(outputs[key])
'''


class GoCommandTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.fixture = Fixture(directory.name)
        self.log = Path(directory.name) / 'go.log'
        script = Path(directory.name) / 'fake go.py'
        script.write_text(FAKE_GO, encoding='utf-8')
        self.go = f'"{sys.executable}" "{script}"'
        linked = self.fixture.cache / 'example.com/linked@v1.2.0'
        other = self.fixture.cache / 'example.com/other/v2@v2.0.1'
        amd64 = f'example.com/linked\tv1.2.0\t\t{linked}\n\n' * 2
        arm64 = amd64 + f'example.com/other/v2\tv2.0.1\t\t{other}\n'
        tool = self.fixture.cache / 'example.com/tool@v0.3.0'
        graph = (f'example.com/linked\tv1.2.0\t\t{linked}\nexample.com/other/v2\tv2.0.1\t\t{other}\n'
                 f'example.com/tool\tv0.3.0\t\t{tool}\nexample.com/testonly\tv1.0.0\t\t\n')
        self.environment = {'FAKE_GO_LOG': str(self.log), 'GOFLAGS': '-mod=mod', 'GOWORK': '/elsewhere/go.work',
                            'FAKE_GO_OUTPUTS': json.dumps({'amd64': amd64, 'arm64': arm64, 'graph': graph})}

    def run_main(self, **environment):
        stdout, stderr = io.StringIO(), io.StringIO()
        with mock.patch.dict(os.environ, dict(self.environment, **environment)), \
                contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            status = dependencies.main(['--go', self.go, '--root', str(self.fixture.root)])
        calls = [json.loads(line) for line in self.log.read_text().splitlines()]
        return status, stdout.getvalue(), stderr.getvalue(), calls

    def test_lists_both_release_targets_read_only(self):
        status, stdout, stderr, calls = self.run_main()
        self.assertEqual((status, stderr), (0, ''))
        self.assertIn('2 modules linked on linux/amd64 and linux/arm64, 4 in the module graph, 3 notices', stdout)
        self.assertEqual(len(calls), 3)
        for call, arch in zip(calls, ('amd64', 'arm64')):
            self.assertEqual(call['args'], ['list', '-mod=readonly', '-deps', '-f', dependencies.DEPS_FORMAT, './cmd/server'])
            self.assertEqual(call['env'], {'CGO_ENABLED': '0', 'GOOS': 'linux', 'GOARCH': arch,
                                           'GOWORK': 'off', 'GOFLAGS': '-mod=mod'})
            self.assertEqual(Path(call['cwd']), self.fixture.root.resolve())
        self.assertEqual(calls[2]['args'], ['list', '-mod=readonly', '-m', '-f', dependencies.GRAPH_FORMAT, 'all'])
        self.assertEqual(calls[2]['env']['GOWORK'], 'off')

    def test_drift_fails_with_every_problem(self):
        self.fixture.edit('docs/dependencies.md', '| example.com/tool | v0.3.0 |', '| example.com/tool | v0.2.0 |')
        self.fixture.edit('docs/dependencies.md', 'v1.2.0_LICENSE) | Yes |', 'v1.2.0_LICENSE) | No |')
        self.fixture.edit('docs/dependencies.md', 'v2.0.1_LICENSE.md) | Yes |', 'v2.0.1_LICENSE.md) | No |')
        (self.fixture.root / 'docs/licenses/example.com_tool@v0.3.0_LICENSE').write_bytes(b'edited\n')
        status, _, stderr, _ = self.run_main()
        self.assertEqual(status, 1)
        self.assertIn('example.com/tool is pinned at v0.2.0, but the module graph selects v0.3.0', stderr)
        self.assertIn('example.com/linked is linked on linux/amd64 and linux/arm64, so its Runtime must be Yes', stderr)
        self.assertIn('example.com/other/v2 is linked on linux/arm64, so its Runtime must be Yes', stderr)
        # Only linked modules are compared with upstream, whatever their rows say.
        self.assertNotIn('differs', stderr)
        (self.fixture.root / 'docs/licenses/example.com_other_v2@v2.0.1_LICENSE.md').write_bytes(b'edited\n')
        status, _, stderr, _ = self.run_main()
        self.assertIn('example.com_other_v2@v2.0.1_LICENSE.md differs from LICENSE.md', stderr)

    def test_go_failure_stops_the_check(self):
        with self.assertRaises(ValueError) as caught:
            self.run_main(FAKE_GO_FAIL='1')
        self.assertTrue(str(caught.exception).startswith('CGO_ENABLED=0 GOOS=linux GOARCH=amd64 '), caught.exception)
        self.assertIn(' list -mod=readonly -deps ./cmd/server failed with status 1:\n'
                      'go: updates to go.sum needed', str(caught.exception))

    def test_missing_go_command(self):
        with self.assertRaisesRegex(ValueError, 'Cannot run'):
            dependencies.main(['--go', str(self.fixture.root / 'no-go'), '--root', str(self.fixture.root)])
        with self.assertRaisesRegex(ValueError, 'empty'):
            dependencies.main(['--go', ' ', '--root', str(self.fixture.root)])


if __name__ == '__main__':
    unittest.main()
