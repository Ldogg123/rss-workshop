#!/usr/bin/env python3
"""Check the dependency table and copied notices against the Go module graph.

The table in docs/dependencies.md must mark as runtime exactly the modules that
./cmd/server links on linux/amd64 and linux/arm64 with CGo disabled, as release
builds do, and pin every row at the version the module graph selects. Every
module notice in docs/licenses/ must be linked from the table or named by
docs/licensing.md, and the notices of linked modules must equal their upstream
files whenever the module cache holds them. Go runs with -mod=readonly, so the
check never rewrites go.mod or go.sum.
"""

import argparse
import dataclasses
import os
import re
import shlex
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
TARGET = './cmd/server'
ARCHES = ('amd64', 'arm64')
HEADER = ['Module', 'Pinned version', 'Copied license notice', 'Runtime']
SEPARATOR = re.compile(r'\|(\s*:?-{3,}:?\s*\|){4}')
MODULE = re.compile(r'[A-Za-z0-9][A-Za-z0-9._~/-]*')
VERSION = re.compile(r'v[0-9][A-Za-z0-9.+-]*')
LINK = re.compile(r'\[[^\]]*\]\(licenses/([^)\s]+)\)')
# A copied module notice is named <module path, / as _>@<version>_<upstream file>.
# A guide may give it bare, link to it in licenses/ or give its docs/licenses/
# path, so a / may precede it.
NOTICE = re.compile(r'(?<![\w.~@-])[A-Za-z0-9][\w.~-]*@v[0-9][A-Za-z0-9.+-]*_[\w.-]*[\w-]')
# Upstream files at a module's root that a copy must carry: any name that starts
# with one of these words, in any case, such as NOTICES or LICENSE2, except Go
# source. AUTHORS and PATENTS are not notices the licenses require, and the
# repository does not copy them.
UPSTREAM = re.compile(r'(?i)(licen[cs]e|copying|notice)[\w.-]*')
DEPS_FORMAT = '{{with .Module}}{{if not .Main}}{{.Path}}\t{{.Version}}\t{{with .Replace}}{{.Path}} {{.Version}}{{end}}\t{{.Dir}}{{end}}{{end}}'
GRAPH_FORMAT = '{{if not .Main}}{{.Path}}\t{{.Version}}\t{{with .Replace}}{{.Path}} {{.Version}}{{end}}\t{{.Dir}}{{end}}'


@dataclasses.dataclass
class Module:
    version: str
    directory: str = ''
    replacement: str = ''
    arches: set = dataclasses.field(default_factory=set)


@dataclasses.dataclass
class Row:
    line: int
    module: str
    version: str
    notices: list
    runtime: bool


def prefix(module, version):
    return module.replace('/', '_') + '@' + version + '_'


def cells(line):
    line = line.strip()
    if not (line.startswith('|') and line.endswith('|')):
        return None
    return [cell.strip() for cell in line[1:-1].split('|')]


def parse_table(text):
    """Return the dependency table's rows and any problems with its format."""
    lines = text.splitlines()
    start = next((number for number, line in enumerate(lines) if cells(line) == HEADER), None)
    if start is None:
        raise ValueError('docs/dependencies.md has no | ' + ' | '.join(HEADER) + ' | table.')
    if start + 1 >= len(lines) or not SEPARATOR.fullmatch(lines[start + 1].strip()):
        raise ValueError(f'docs/dependencies.md:{start + 2}: expected the table separator row.')
    rows, problems, seen = [], [], set()
    for number, line in enumerate(lines[start + 2:], start + 3):
        row = cells(line)
        if row is None:
            break
        where = f'docs/dependencies.md:{number}'
        if len(row) != 4:
            problems.append(f'{where}: expected 4 cells, found {len(row)}.')
            continue
        module, version, notice, runtime = row
        if not MODULE.fullmatch(module):
            problems.append(f'{where}: {module!r} is not a module path.')
            continue
        if module in seen:
            problems.append(f'{where}: {module} has a second row.')
            continue
        seen.add(module)
        if not VERSION.fullmatch(version):
            problems.append(f'{where}: {module} has no pinned version.')
        if runtime not in ('Yes', 'No'):
            problems.append(f'{where}: {module} needs Runtime Yes or No, not {runtime!r}.')
        notices = LINK.findall(notice)
        if not notices or LINK.sub('', notice).strip(' ,'):
            problems.append(f'{where}: {module} needs its notices as comma-separated links into licenses/.')
        rows.append(Row(number, module, version, notices, runtime == 'Yes'))
    return rows, problems


def mentions(path):
    if not path.is_file():
        return set()
    return set(NOTICE.findall(path.read_text(encoding='utf-8')))


def check(root, runtime, graph):
    """Compare the tree at root with the linked modules and the module graph.

    runtime and graph map module paths to Module values. Returns the problems,
    notes about checks that could not run, and how many notices matched their
    upstream bytes.
    """
    problems, notes = [], []
    rows, table_problems = parse_table((root / 'docs/dependencies.md').read_text(encoding='utf-8'))
    problems += table_problems
    rows_by_module = {row.module: row for row in rows}
    licenses = root / 'docs/licenses'
    files = {path.name for path in licenses.iterdir() if path.is_file()}

    for path, module in sorted(runtime.items()):
        arches = ' and '.join('linux/' + arch for arch in sorted(module.arches)) or 'the target'
        row = rows_by_module.get(path)
        if module.replacement:
            problems.append(f'{path} is replaced by {module.replacement}; the table cannot record a '
                            'replacement, so extend this check before shipping one.')
        if row is None:
            problems.append(f'{path} {module.version} is linked on {arches} but has no row in '
                            'docs/dependencies.md; add one and copy its notices to docs/licenses/.')
        elif not row.runtime:
            problems.append(f'docs/dependencies.md:{row.line}: {path} is linked on {arches}, '
                            'so its Runtime must be Yes.')

    for row in rows:
        where = f'docs/dependencies.md:{row.line}'
        selected = graph.get(row.module)
        if selected is None:
            problems.append(f'{where}: {row.module} is not in the module graph; remove its row and notices.')
            continue
        if row.version != selected.version:
            problems.append(f'{where}: {row.module} is pinned at {row.version}, but the module graph '
                            f'selects {selected.version}.')
        if row.runtime and row.module not in runtime:
            problems.append(f'{where}: {row.module} is marked Runtime Yes, but neither linux/amd64 '
                            'nor linux/arm64 links it.')
        expected = prefix(row.module, row.version)
        for name in row.notices:
            if not name.startswith(expected) or name == expected:
                problems.append(f'{where}: {row.module} links licenses/{name}, which is not named '
                                f'{expected}<upstream file>.')
            elif name not in files:
                problems.append(f'{where}: {row.module} links licenses/{name}, which does not exist.')

    linked = {name for row in rows for name in row.notices}
    named = mentions(root / 'docs/licensing.md')
    readme = mentions(licenses / 'README.md')
    for document, names in (('docs/licensing.md', named), ('docs/licenses/README.md', readme)):
        for name in sorted(names - files):
            problems.append(f'{document} names {name}, which docs/licenses/ does not contain.')

    for name in sorted(files):
        if '@' not in name:
            continue
        if name not in linked and name not in named:
            problems.append(f'docs/licenses/{name} is neither linked from docs/dependencies.md nor named '
                            'in docs/licensing.md.')
        escaped, _, rest = name.partition('@')
        version = rest.partition('_')[0]
        owners = [row for row in rows if row.module.replace('/', '_') == escaped]
        if not owners:
            problems.append(f'docs/licenses/{name} belongs to no module in docs/dependencies.md.')
        elif all(row.version != version for row in owners):
            problems.append(f'docs/licenses/{name} is for {version}, but docs/dependencies.md pins '
                            f'{owners[0].module} at {owners[0].version}.')

    compared = 0
    for path, module in sorted(runtime.items()):
        row = rows_by_module.get(path)
        if row is None or row.version != module.version or module.replacement:
            continue
        directory = Path(module.directory) if module.directory else None
        if directory is None or not directory.is_dir():
            notes.append(f'{path} {module.version} is not in the module cache, so its notices were not '
                         'compared with upstream.')
            continue
        expected = prefix(path, module.version)
        copied = set()
        for name in row.notices:
            if not name.startswith(expected) or name == expected or name not in files:
                continue
            upstream = name[len(expected):]
            copied.add(upstream)
            source = directory / upstream
            if not source.is_file():
                problems.append(f'docs/licenses/{name}: {path} {module.version} has no {upstream}.')
            elif source.read_bytes() != (licenses / name).read_bytes():
                problems.append(f'docs/licenses/{name} differs from {upstream} in {path} {module.version}.')
            else:
                compared += 1
        for entry in sorted(directory.iterdir(), key=lambda entry: entry.name):
            if (entry.is_file() and UPSTREAM.fullmatch(entry.name) and not entry.name.endswith('.go')
                    and entry.name not in copied):
                problems.append(f'{path} {module.version} ships {entry.name}, which its row does not link; '
                                f'copy it to docs/licenses/{expected}{entry.name}.')
    return problems, notes, compared


def go_output(go, root, args, **environment):
    command = go + args
    try:
        result = subprocess.run(command, cwd=root, env=dict(os.environ, GOWORK='off', **environment),
                                capture_output=True, text=True, timeout=600)
    except OSError as error:
        raise ValueError(f'Cannot run {shlex.join(go)}: {error.strerror or error}') from None
    if result.returncode:
        # Name the command without its output template, which only adds noise.
        shown = [arg for index, arg in enumerate(args) if '-f' not in (arg, args[index - 1] if index else '')]
        settings = ''.join(f'{key}={value} ' for key, value in environment.items())
        detail = result.stderr.strip()[-4000:]
        raise ValueError(f'{settings}{shlex.join(go + shown)} failed with status {result.returncode}:\n{detail}')
    return result.stdout


def parse_modules(output, arch=None):
    modules = {}
    for line in output.splitlines():
        if not line.strip():
            continue
        fields = line.split('\t', 3)
        if len(fields) != 4 or not fields[0]:
            raise ValueError(f'Unexpected go list output: {line!r}')
        path, version, replacement, directory = fields
        modules[path] = Module(version, directory, replacement, {arch} if arch else set())
    return modules


def linked_modules(go, root):
    """Return the modules ./cmd/server links on each supported architecture."""
    linked = {}
    for arch in ARCHES:
        # GOWORK is off and the build list is read-only: the table documents
        # this module's own go.mod, never a developer's workspace.
        output = go_output(go, root, ['list', '-mod=readonly', '-deps', '-f', DEPS_FORMAT, TARGET],
                           CGO_ENABLED='0', GOOS='linux', GOARCH=arch)
        for path, module in parse_modules(output, arch).items():
            linked.setdefault(path, module).arches.add(arch)
    return linked


def module_graph(go, root):
    return parse_modules(go_output(go, root, ['list', '-mod=readonly', '-m', '-f', GRAPH_FORMAT, 'all']))


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split('\n\n')[0])
    parser.add_argument('--go', default='go', help='Go command, as Make passes $(GO)')
    parser.add_argument('--root', type=Path, default=ROOT, help='repository root')
    args = parser.parse_args(argv)
    go = shlex.split(args.go)
    if not go:
        raise ValueError('The Go command is empty.')
    root = args.root.resolve()
    runtime = linked_modules(go, root)
    graph = module_graph(go, root)
    problems, notes, compared = check(root, runtime, graph)
    for note in notes:
        print('Note: ' + note)
    if problems:
        print('docs/dependencies.md or docs/licenses/ does not match the Go module graph:', file=sys.stderr)
        for problem in problems:
            print('- ' + problem, file=sys.stderr)
        print('See "Checking the table" in docs/dependencies.md.', file=sys.stderr)
        return 1
    targets = ' and '.join('linux/' + arch for arch in ARCHES)
    print(f'Dependency notices match: {len(runtime)} modules linked on {targets}, {len(graph)} in the '
          f'module graph, {compared} notices identical to upstream.')
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print('Dependency check failed: ' + str(error), file=sys.stderr)
        sys.exit(2)
