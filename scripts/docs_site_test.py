#!/usr/bin/env python3
"""Offline checks for the documentation site's staging and link checks."""

import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('docs_site', Path(__file__).with_name('docs-site.py'))
docs_site = importlib.util.module_from_spec(spec)
spec.loader.exec_module(docs_site)

SITE = 'https://docs.example/project/'
REPO = 'https://github.com/example/project'
CONFIG = f'''[project]
site_name = "Project"
site_url = "{SITE}"
repo_url = "{REPO}"
docs_dir = "build/src"
site_dir = "build/site"
nav = [
  {{ "Home" = "index.md" }},
  {{ "Guides" = [ "start.md", "guide.md", {{ "Other" = "other.md" }}, "examples/README.md" ] }},
  {{ "Changelog" = "changelog.md" }},
  {{ "Elsewhere" = "https://example.com/" }},
]
'''
README = '''# Project

Intro with a [guide](docs/guide.md) and the [examples](docs/examples/).

## Quickstart

Run it, then read [the guide's limits](docs/guide.md#limits--failures) or [first feed](#your-first-feed).

![Dashboard](docs/screenshots/dashboard.png)

<details>
<summary>More</summary>

Hidden **text**.

</details>

## Your first feed

See [Quickstart](#quickstart), the [license](LICENSE) and the [versions](#versioning).

## Versioning

Stable.
'''
GUIDE = '''# Guide

See [other](other.md), [its section](other.md#new-1), [here](#limits--failures), [the override](../compose.yaml),
[a notice](licenses/GPL-3), the [notices](licenses/), the [changelog](../CHANGELOG.md#v100--2026-01-01),
the [examples](examples/), [a recipe](examples/demo-recipe.json), the [demo](examples/demo-site/),
[quickstart](../README.md#quickstart) and [versioning](../README.md#versioning).

`[not a link](missing.md)` stays, as does this block:

```sh
# Not a heading
cat [also not](missing.md)
```

## Limits & failures

- First
  - Nested
- Second
'''
OTHER = '''# Other

## New

## New

[Back](guide.md) and [site](https://docs.example/project/guide/) and <https://docs.example/project/examples/hosted/demo-recipe.json>
and [repository](https://github.com/example/project/blob/main/compose.yaml).
'''
RECIPE = '{"feeds": [{"url": "http://127.0.0.1:8000/index.html"}]}\n'


class Fixture:
    def __init__(self, directory):
        self.root = Path(directory)
        self.write('website/zensical.toml', CONFIG)
        self.write('website/pages/index.md', '---\nhide:\n  - toc\n---\n# Home\n\n[Guide](../../docs/guide.md)\n')
        self.write('website/pages/start.md', '# Start\n\n<!-- include README.md#quickstart -->\n'
                                             '<!-- include README.md#your-first-feed -->\n')
        self.write('website/pages/assets/site.css', 'body {}\n')
        self.write('README.md', README)
        self.write('CHANGELOG.md', '# Changelog\n\n## v1.0.0 — 2026-01-01\n')
        self.write('LICENSE', 'MIT\n')
        self.write('compose.yaml', 'services: {}\n')
        self.write('docs/guide.md', GUIDE)
        self.write('docs/other.md', OTHER)
        self.write('docs/licenses/GPL-3', 'GPL\n')
        self.write('docs/screenshots/dashboard.png', 'png')
        self.write('docs/examples/README.md', '# Examples\n\n[Guide](../guide.md)\n')
        self.write('docs/examples/demo-recipe.json', RECIPE)
        self.write('docs/examples/demo-site/index.html', '<p>demo</p>\n')

    def write(self, name, text):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding='utf-8')

    def edit(self, name, old, new):
        text = (self.root / name).read_text(encoding='utf-8')
        if old not in text:
            raise AssertionError(f'{name} does not contain {old!r}')
        self.write(name, text.replace(old, new))

    def prepare(self):
        site, project, staged = docs_site.prepare(self.root)
        return site, project, {path: content.decode('utf-8') for path, content in staged.items()
                               if path.endswith(('.md', '.json'))} | {path: content for path, content in staged.items()
                                                                     if not path.endswith(('.md', '.json'))}


class AnchorTest(unittest.TestCase):
    def anchors(self, text):
        return [heading.anchor for heading in docs_site.headings(text.split('\n'))]

    def test_anchors_follow_github(self):
        self.assertEqual(self.anchors('# v1.3.0 — 2026-11-07\n## The `MAX_ITEMS` setting\n### Limits & failures\n'
                                      '## What’s new?\n## A [linked](x.md) *word* and snake_case\n## Trailing ##'),
                         ['v130--2026-11-07', 'the-max_items-setting', 'limits--failures', 'whats-new',
                          'a-linked-word-and-snake_case', 'trailing'])

    def test_repeated_headings_are_numbered_with_hyphens(self):
        self.assertEqual(self.anchors('## New\n## Fixes\n## New\n## New\n## New-1'), ['new', 'fixes', 'new-1', 'new-2', 'new-1-1'])

    def test_code_is_fenced_or_indented(self):
        lines = ('para\n    still the paragraph\n\n    code\n\n    more code\ntext\n\n- item\n\n    item text\n\n      item code\n'
                 '  ```\n\n  fenced\n  ```\n1. one\n\n    one text\n\n       one code').split('\n')
        self.assertEqual([lines[index] for index in sorted(docs_site.code_lines(lines))],
                         ['    code', '    more code', '      item code', '  ```', '', '  fenced', '  ```', '       one code'])

    def test_table_rows_are_found(self):
        lines = 'a | b\n\n| a | b |\n|---|:-:|\n| 1 | 2 |\n\ntext\n---\n\n```\n| a |\n| - |\n```\nc | d\n- | -\ne | f'.split('\n')
        self.assertEqual(sorted(docs_site.table_lines(lines, docs_site.code_lines(lines))), [2, 3, 4, 13, 14, 15])

    def test_code_blocks_and_front_matter_hold_no_headings(self):
        text = '---\ntitle: x\n# not this\n---\n# Real\n```sh\n# Edit .env first\n```\n~~~\n## nor this\n~~~\n## After { #chosen }'
        self.assertEqual(self.anchors(text), ['real', 'chosen'])


class IndentTest(unittest.TestCase):
    def assertIndent(self, github, expected):
        self.assertEqual(docs_site.reindent(github.split('\n')), expected.split('\n'))

    def test_nested_lists_and_continuations_take_four_spaces(self):
        self.assertIndent('- one\n  - two\n    - three\n\n  continued\n- four\n\ntext',
                          '- one\n    - two\n        - three\n\n    continued\n- four\n\ntext')
        self.assertIndent('1. one\n\n   continued\n   - nested\n2. two', '1. one\n\n    continued\n    - nested\n2. two')

    def test_code_in_a_list_item_moves_as_a_block(self):
        self.assertIndent('- one\n\n  ```sh\n  run\n    indented\n  ```\n- two',
                          '- one\n\n    ```sh\n    run\n      indented\n    ```\n- two')

    def test_text_outside_lists_is_untouched(self):
        text = 'para\n\n    indented code\n\n```sh\n- not a list\n  - nor this\n```\n\n- wrapped item\ncontinues lazily\n\n| a |\n| - |'
        self.assertIndent(text, text)

    def test_a_list_may_interrupt_a_paragraph(self):
        self.assertIndent('Intro:\n- one\n- two\nText', 'Intro:\n\n- one\n- two\nText')
        self.assertIndent('Intro:\n1. one\n   - nested\n2. two', 'Intro:\n\n1. one\n    - nested\n2. two')

    def test_a_list_of_the_other_kind_starts_a_new_list(self):
        # GitHub ends the bulleted list here and starts a numbered one at 2.
        self.assertIndent('- one\n- two\nText\n2. numbered\n- bulleted', '- one\n- two\nText\n\n2. numbered\n\n- bulleted')
        self.assertIndent('- one\n  - nested\n  2. numbered', '- one\n    - nested\n\n    2. numbered')

    def test_a_wrapped_line_that_begins_with_a_number_stays_text(self):
        self.assertIndent('- Keeps at most\n  50. Then it continues\n  and ends here.\n- two',
                          '- Keeps at most\n    50\\. Then it continues\n    and ends here.\n- two')
        self.assertIndent('1. first\n   7. still the first item\n2. second', '1. first\n    7\\. still the first item\n2. second')
        self.assertIndent('Released in\n2026. More text', 'Released in\n2026\\. More text')
        # A list that starts at 1 may interrupt a paragraph, and a blank line lets any number start one.
        self.assertIndent('- one\n  1. nested\n\n  7. seventh', '- one\n    1. nested\n\n    7. seventh')

    def test_indented_code_is_not_read_as_a_list(self):
        text = 'para\n\n    - not a list\n      - nor this\n    3. nor this\n\ntext'
        self.assertIndent(text, text)
        self.assertIndent('- one\n\n      - code\n        more\n\n  text\n- two',
                          '- one\n\n        - code\n          more\n\n    text\n- two')


class StageTest(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.fixture = Fixture(directory.name)

    def assertProblems(self, *expected):
        site, _, _ = self.fixture.prepare()
        problems = sorted(set(site.problems))
        self.assertEqual(len(problems), len(expected), problems)
        for problem, fragment in zip(problems, sorted(expected)):
            self.assertIn(fragment, problem)

    def test_the_fixture_is_consistent(self):
        site, _, staged = self.fixture.prepare()
        self.assertEqual(site.problems, [])
        self.assertEqual(sorted(staged), ['assets/site.css', 'changelog.md', 'examples/README.md', 'examples/demo-recipe.json',
                                          'examples/demo-site/index.html', 'examples/hosted/demo-recipe.json', 'guide.md',
                                          'index.md', 'other.md', 'screenshots/dashboard.png', 'start.md'])

    def test_links_follow_the_staged_copies(self):
        _, _, staged = self.fixture.prepare()
        guide = staged['guide.md']
        for link in ('[other](other.md)', '[its section](other.md#new-1)', '[here](#limits--failures)',
                     f'[the override]({REPO}/blob/main/compose.yaml)', f'[a notice]({REPO}/blob/main/docs/licenses/GPL-3)',
                     f'[notices]({REPO}/tree/main/docs/licenses)', '[changelog](changelog.md#v100--2026-01-01)',
                     '[examples](examples/README.md)', '[a recipe](examples/demo-recipe.json)',
                     '[demo](examples/demo-site/index.html)', '[quickstart](start.md#quickstart)',
                     f'[versioning]({REPO}/blob/main/README.md#versioning)'):
            self.assertIn(link, guide)
        self.assertIn('`[not a link](missing.md)` stays', guide)
        self.assertIn('# Not a heading\ncat [also not](missing.md)', guide)
        self.assertIn('[Guide](../guide.md)', staged['examples/README.md'])
        self.assertIn('[Guide](guide.md)', staged['index.md'])

    def test_pages_record_their_source_and_headings_keep_github_anchors(self):
        _, _, staged = self.fixture.prepare()
        self.assertTrue(staged['guide.md'].startswith('---\nsource_path: docs/guide.md\n---\n# Guide { #guide }\n'))
        self.assertIn('## Limits & failures { #limits--failures }\n\n- First\n    - Nested\n- Second\n', staged['guide.md'])
        self.assertIn('## New { #new }\n\n## New { #new-1 }\n', staged['other.md'])
        self.assertTrue(staged['changelog.md'].startswith('---\nsource_path: CHANGELOG.md\n---\n'))
        self.assertTrue(staged['index.md'].startswith('---\nhide:\n  - toc\nsource_path: website/pages/index.md\n---\n'))

    def test_included_sections_are_published_with_their_links(self):
        _, _, staged = self.fixture.prepare()
        start = staged['start.md']
        self.assertIn('# Start { #start }\n\n## Quickstart { #quickstart }\n', start)
        self.assertIn('[the guide\'s limits](guide.md#limits--failures) or [first feed](#your-first-feed)', start)
        self.assertIn('\n[![Dashboard](screenshots/dashboard.png)](screenshots/dashboard.png)\n', start)
        self.assertIn('<details markdown="1">\n<summary>More</summary>', start)
        self.assertIn(f'[Quickstart](#quickstart), the [license]({REPO}/blob/main/LICENSE) and the '
                      f'[versions]({REPO}/blob/main/README.md#versioning)', start)
        self.assertNotIn('Stable.', start)
        self.assertNotIn('Intro with', start)

    def test_images_open_at_full_size(self):
        # GitHub links every image to its file; the site's copy does the same.
        self.fixture.write('docs/other.md', '# Other\n\n## New\n\n## New\n\n'
                           '![One](screenshots/dashboard.png "Title") and ![two](screenshots/dashboard.png).\n\n'
                           '[![Linked](screenshots/dashboard.png)](guide.md) and `![code](missing.png)` and '
                           '![badge](https://img.example/badge.svg)\n')
        self.fixture.edit('website/pages/index.md', '[Guide]', '![Shot](../../docs/screenshots/dashboard.png){ .wide }\n\n[Guide]')
        site, _, staged = self.fixture.prepare()
        self.assertEqual(site.problems, [])
        self.assertIn('\n[![One](screenshots/dashboard.png "Title")](screenshots/dashboard.png) and '
                      '[![two](screenshots/dashboard.png)](screenshots/dashboard.png).\n', staged['other.md'])
        self.assertIn('\n[![Linked](screenshots/dashboard.png)](guide.md) and `![code](missing.png)` and '
                      '![badge](https://img.example/badge.svg)\n', staged['other.md'])
        self.assertIn('\n[![Shot](screenshots/dashboard.png){ .wide }](screenshots/dashboard.png)\n', staged['index.md'])

    def test_a_footnote_is_not_a_link_reference(self):
        self.fixture.write('docs/other.md', OTHER + '\nA note[^1] and a [reference][ref].\n\n'
                           '[^1]: The note, with [a guide](guide.md) and [a file](../compose.yaml).\n\n[ref]: guide.md\n')
        site, _, staged = self.fixture.prepare()
        self.assertEqual(site.problems, [])
        self.assertIn(f'\n[^1]: The note, with [a guide](guide.md) and [a file]({REPO}/blob/main/compose.yaml).\n\n[ref]: guide.md\n',
                      staged['other.md'])
        self.fixture.edit('docs/other.md', '[ref]: guide.md', '[ref]: gone.md')
        self.assertProblems('docs/other.md:14: link gone.md points to docs/gone.md')

    def test_indented_code_keeps_its_text(self):
        self.fixture.write('docs/other.md', '# Other\n\n## New\n\n## New\n\nRun:\n\n    cat [not a link](missing.md)\n    <details>\n\n'
                           '- Step\n\n      see [nor this](missing.md)\n\n  but [this is](guide.md)\n')
        site, _, staged = self.fixture.prepare()
        self.assertEqual(site.problems, [])
        self.assertIn('Run:\n\n    cat [not a link](missing.md)\n    <details>\n\n'
                      '- Step\n\n        see [nor this](missing.md)\n\n    but [this is](guide.md)\n', staged['other.md'])

    def test_an_escaped_pipe_in_a_table_cell_s_code_is_a_pipe(self):
        # GitHub shows `a | b` for this cell; Python-Markdown would keep the backslash.
        self.fixture.write('docs/other.md', '# Other\n\n## New\n\n## New\n\n| Selector | Meaning |\n| --- | --- |\n'
                           '| `//a \\| //b` | either \\| both, see [other](guide.md) and `x \\| y` |\n\n'
                           'Outside a table `a \\| b` keeps its backslash.\n\n```\n| `a \\| b` |\n| --- |\n```\n')
        _, _, staged = self.fixture.prepare()
        self.assertIn('\n| `//a | //b` | either \\| both, see [other](guide.md) and `x | y` |\n\n'
                      'Outside a table `a \\| b` keeps its backslash.\n\n```\n| `a \\| b` |\n| --- |\n```\n', staged['other.md'])

    def test_the_site_offers_the_recipes_with_its_own_address(self):
        _, _, staged = self.fixture.prepare()
        self.assertEqual(staged['examples/demo-recipe.json'], RECIPE)
        self.assertEqual(staged['examples/hosted/demo-recipe.json'],
                         '{"feeds": [{"url": "https://docs.example/project/examples/demo-site/index.html"}]}\n')
        self.fixture.edit('docs/examples/demo-recipe.json', '127.0.0.1:8000', 'news.example')
        self.assertProblems('docs/examples/demo-recipe.json: expected every feed address to start with http://127.0.0.1:8000/')

    def test_broken_links_are_reported_where_they_are_written(self):
        self.fixture.edit('docs/guide.md', '[other](other.md)', '[other](gone.md)')
        self.fixture.edit('docs/guide.md', 'other.md#new-1', 'other.md#new_1')
        self.fixture.edit('docs/guide.md', '(#limits--failures)', '(#limits-failures)')
        self.fixture.edit('docs/guide.md', '../compose.yaml', '../../compose.yaml')
        self.fixture.edit('README.md', 'docs/screenshots/dashboard.png', 'docs/screenshots/missing.png')
        self.fixture.edit('docs/other.md', 'project/guide/)', 'project/guides/)')
        self.fixture.edit('docs/other.md', 'blob/main/compose.yaml', 'blob/main/compose.yml')
        self.fixture.edit('docs/other.md', 'hosted/demo-recipe.json>', 'hosted/other-recipe.json>')
        self.assertProblems(
            'README.md:9: link docs/screenshots/missing.png points to docs/screenshots/missing.png, which the repository does not have.',
            'docs/guide.md:3: link ../../compose.yaml leaves the repository.',
            'docs/guide.md:3: link #limits-failures names an anchor that docs/guide.md does not have.',
            'docs/guide.md:3: link other.md#new_1 names an anchor that docs/other.md does not have.',
            'docs/guide.md:3: link gone.md points to docs/gone.md, which the repository does not have.',
            'docs/other.md:7: link https://docs.example/project/guides/ points to a page or file the site does not publish.',
            'docs/other.md:7: link https://docs.example/project/examples/hosted/other-recipe.json points to a page',
            'docs/other.md:8: link https://github.com/example/project/blob/main/compose.yml points to compose.yml, which the repository')

    def test_guides_github_shows_but_the_site_does_not_are_checked_too(self):
        self.fixture.write('AGENTS.md', '# Guide for agents\n\n[Gone](docs/gone.md)\n')
        self.fixture.write('docs/licenses/README.md', '# Notices\n\n[Gone](GPL-2)\n')
        self.fixture.write('docs/licenses/upstream_LICENSE.md', '[Upstream text](not-ours.md)\n')
        self.assertProblems('AGENTS.md:3: link docs/gone.md', 'docs/licenses/README.md:3: link GPL-2')

    def test_an_include_must_name_a_section(self):
        self.fixture.edit('website/pages/start.md', 'README.md#quickstart', 'README.md#quick-start')
        self.assertProblems('website/pages/start.md:3: include names a section that README.md does not have: #quick-start.')

    def test_every_page_must_be_in_the_navigation(self):
        self.fixture.write('docs/new.md', '# New guide\n')
        self.fixture.edit('website/zensical.toml', '"start.md", ', '"start.md", "gone.md", ')
        self.assertProblems('website/zensical.toml: nav lists gone.md, which is not a page of the site.',
                            'website/zensical.toml: nav does not list new.md (docs/new.md), so readers could not find it.')

    def test_two_sources_cannot_share_a_page(self):
        self.fixture.write('docs/changelog.md', '# Another changelog\n')
        site, _, _ = self.fixture.prepare()
        self.assertIn('docs/changelog.md: would be published as changelog.md, which CHANGELOG.md already is.', site.problems)

    def test_writing_touches_only_what_changed(self):
        target = self.fixture.root / 'website/build/src'
        _, _, staged = docs_site.prepare(self.fixture.root)
        self.assertEqual(docs_site.write(target, staged), len(staged))
        self.assertEqual(docs_site.write(target, staged), 0)
        (target / 'old/stale.md').parent.mkdir()
        (target / 'old/stale.md').write_text('stale')
        self.fixture.edit('docs/other.md', '# Other', '# Another')
        _, _, staged = docs_site.prepare(self.fixture.root)
        self.assertEqual(docs_site.write(target, staged), 2)
        self.assertFalse((target / 'old').exists())
        self.assertIn('# Another', (target / 'other.md').read_text())


PAGE = '''<!doctype html><html><head><link rel="stylesheet" href="{base}assets/site.css">
<link rel="canonical" href="https://docs.example/project/{url}"></head><body>
<a href="{base}">Home</a><article>{body}</article></body></html>'''


class VerifyTest(unittest.TestCase):
    """A hand-built site stands in for the generator's output."""

    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.fixture = Fixture(directory.name)
        self.site = self.fixture.root / 'website/build/site'
        bodies = {
            '': '<h1 id="home">Home</h1><p><a href="guide/">Guide</a></p>',
            'start/': '<h1 id="start">Start</h1><h2 id="quickstart">Quickstart</h2><h2 id="your-first-feed">First</h2>'
                      '<p><img src="../screenshots/dashboard.png" alt=""><a href="../guide/#limits--failures">limits</a></p>',
            'guide/': '<h1 id="guide">Guide</h1><h2 id="limits--failures">Limits</h2><pre><code>- not a leak\n** nor this</code></pre>'
                      f'<p><a href="../other/#new-1">other</a> <a href="{REPO}/blob/main/compose.yaml">file</a> '
                      '<a href="https://example.com/">out</a> <a href="../examples/demo-site/index.html">demo</a></p>',
            'other/': '<h1 id="other">Other</h1><h2 id="new">New</h2><h2 id="new-1">New</h2>'
                      f'<p><a href="{SITE}guide/">site</a></p>',
            'examples/': '<h1 id="examples">Examples</h1><p><a href="hosted/demo-recipe.json">recipe</a></p>',
            'changelog/': '<h1 id="changelog">Changelog</h1><h2 id="v100--2026-01-01">v1.0.0</h2>',
        }
        for url, body in bodies.items():
            self.page(url, body)
        _, _, staged = docs_site.prepare(self.fixture.root)
        for path, content in staged.items():
            if not path.endswith('.md'):
                (self.site / path).parent.mkdir(parents=True, exist_ok=True)
                (self.site / path).write_bytes(content)

    def page(self, url, body, head=''):
        path = self.site / url / 'index.html'
        path.parent.mkdir(parents=True, exist_ok=True)
        text = PAGE.format(base='../' * url.count('/'), url=url, body=body)
        path.write_text(text.replace('</head>', head + '</head>'), encoding='utf-8')

    def verify(self):
        site, project, staged = docs_site.prepare(self.fixture.root)
        summary = docs_site.verify(self.fixture.root, site, project, staged)
        return list(dict.fromkeys(site.problems)), summary

    def test_a_sound_site_passes_with_counts(self):
        problems, summary = self.verify()
        self.assertEqual(problems, [])
        self.assertIn('6 built pages; 12 internal links (2 with an anchor), 12 GitHub heading anchors present, '
                      '7 images, styles and scripts (none from another site), 1 links to repository files, 0 edit links, '
                      '1 external links (not fetched)', summary)

    def test_broken_links_anchors_and_images_fail(self):
        self.page('other/', '<h1 id="other">Other</h1><h2 id="new">New</h2><h2 id="new_1">New</h2>'
                            '<p><a href="../gone/">gone</a> <a href="../guide/#nowhere">anchor</a> <img src="../missing.png">'
                            f'<a href="{REPO}/blob/main/gone.yaml">file</a></p>')
        problems, _ = self.verify()
        self.assertEqual(problems, [
            'guide/index.html: links to ../other/#new-1, but that page has no such anchor.',
            'other/index.html: loads ../missing.png, which the built site does not have.',
            'other/index.html: links to ../gone/, which the built site does not have.',
            'other/index.html: links to ../guide/#nowhere, but that page has no such anchor.',
            f'other/index.html: links to {REPO}/blob/main/gone.yaml, which the repository does not have.',
            'docs/other.md: GitHub gives a heading the anchor #new-1, which other/index.html does not have.'])

    def test_nothing_may_load_from_another_site(self):
        self.page('changelog/', '<h1 id="changelog">Changelog</h1><h2 id="v100--2026-01-01">v1.0.0</h2>'
                                '<p><img src="https://img.example/badge.svg"><img src="data:image/gif;base64,AAAA"></p>',
                  head='<link rel="preconnect" href="https://fonts.gstatic.com"><script src="//cdn.example/x.js"></script>')
        problems, _ = self.verify()
        self.assertEqual(problems, [
            'changelog/index.html: loads https://fonts.gstatic.com from another site when viewed.',
            'changelog/index.html: loads //cdn.example/x.js from another site when viewed.',
            'changelog/index.html: loads https://img.example/badge.svg from another site when viewed.'])

    def test_markdown_left_as_text_fails(self):
        self.page('changelog/', '<h1 id="changelog">Changelog</h1><h2 id="v100--2026-01-01">v1.0.0 { #v100--2026-01-01 }</h2>'
                                '<p>Intro:\n- one\n- two</p><p>A [link](guide.md) and <code>**kept**</code></p>'
                                '<p>Steps:\n1. one\n2. two</p><p>3. three</p><ul><li>Keeps at most\n    50. Then it goes on</li></ul>')
        problems, _ = self.verify()
        self.assertEqual(problems, [
            "changelog/index.html: shows Markdown as text (a heading anchor): 'v1.0.0 { #v100--2026-01-01 }'",
            "changelog/index.html: shows Markdown as text (a list item): 'Intro: - one - two'",
            "changelog/index.html: shows Markdown as text (a link): 'A [link](guide.md) and'",
            "changelog/index.html: shows Markdown as text (a list item): 'Steps: 1. one 2. two'",
            "changelog/index.html: shows Markdown as text (a list item): '3. three'"])

    def test_missing_pages_and_changed_files_fail(self):
        (self.site / 'changelog/index.html').unlink()
        (self.site / 'examples/demo-site/index.html').write_text('<p>edited</p>\n')
        problems, _ = self.verify()
        self.assertEqual(problems, ['CHANGELOG.md: was staged as changelog.md but the build has no changelog/index.html.',
                                    'website/build/site/examples/demo-site/index.html: is missing or differs from its source.'])

    def test_an_unbuilt_site_says_so(self):
        (self.site / 'index.html').unlink()
        problems, _ = self.verify()
        self.assertEqual(problems, ['website/build/site: has no index.html; build the site first (make docs).'])


if __name__ == '__main__':
    unittest.main()
