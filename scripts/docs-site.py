#!/usr/bin/env python3
"""Stage the guides for the documentation site and check their links.

The guides are written for GitHub: they link to each other, to files elsewhere
in the repository and to headings by GitHub's anchors, and some live at the
repository root. The site generator needs one directory of pages instead, so
this script copies them into website/build/src and changes only what a site
needs: links follow the copies, links to files the site does not publish open
them on GitHub, every heading keeps the anchor GitHub gives it, and lists
indented GitHub's way are indented Python-Markdown's way. The guides themselves
are never rewritten.

  check   resolve every link and anchor in the guides as GitHub does; no build
  stage   check, then write website/build/src
  verify  check the built site in website/build/site: links, anchors, images,
          repository links, the navigation and what a page loads when viewed
  serve   stage, run "zensical serve" and stage again when a guide changes

Standard library only; the site generator is pinned in website/requirements.txt.
"""

import argparse
import dataclasses
import html.parser
import os
import posixpath
import re
import signal
import subprocess
import sys
import time
import tomllib
import unicodedata
import urllib.parse
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
CONFIG = 'website/zensical.toml'
BRANCH = 'main'
# Pages and files written only for the site. Everything else is a guide that
# GitHub shows too.
SITE_ONLY = 'website/pages/'
# Root documents keep their place in the repository and get a lower-case name
# on the site.
ROOT_PAGES = {'CHANGELOG.md': 'changelog.md', 'CONTRIBUTING.md': 'contributing.md', 'SECURITY.md': 'security.md'}
# Directories under docs/ published as they are. docs/licenses is left out on
# purpose: the notices are verbatim legal texts, and the site links to them in
# the repository instead of republishing copies.
PUBLISHED = ('docs/screenshots/', 'docs/examples/')
# The shipped recipes fetch the demo site from this address. The site
# publishes the same demo, so it also offers the recipes with its own address.
RECIPES = re.compile(r'docs/examples/([^/]+\.json)')
LOCAL_DEMO = 'http://127.0.0.1:8000/'
DEMO = 'examples/demo-site/'
HOSTED = 'examples/hosted/'
# Front matter key the edit button reads; see website/overrides.
SOURCE_KEY = 'source_path'

FENCE = re.compile(r'^( *)(`{3,}|~{3,})(.*)$')
HEADING = re.compile(r'^(#{1,6})[ \t]+(.*?)[ \t]*$')
CLOSING_HASHES = re.compile(r'[ \t]+#+$')
EXPLICIT_ID = re.compile(r'[ \t]*\{:?[^{}]*#([\w-]+)[^{}]*\}$')
LIST_ITEM = re.compile(r'^( *)([-*+]|\d{1,9}[.)])( +)(?=\S)')
INCLUDE = re.compile(r'^<!-- include ([^\s#]+)#([^\s#]+) -->$')
CODE_SPAN = re.compile(r'(?<!`)(`+)(?!`)(.+?)(?<!`)\1(?!`)')
INLINE_LINK = re.compile(r'\]\(\s*(<[^<>]*>|[^\s()]*(?:\([^\s()]*\)[^\s()]*)*)(\s+(?:"[^"]*"|\'[^\']*\'))?\s*\)')
REFERENCE = re.compile(r'^ {0,3}\[[^\]]+\]:\s*(<[^<>]*>|\S+)')
HTML_ATTRIBUTE = re.compile(r'<[a-zA-Z][^<>]*?\s(?:href|src)="([^"]*)"')
AUTOLINK = re.compile(r'<([a-zA-Z][a-zA-Z0-9+.-]*://[^<>\s]+)>')
SCHEME = re.compile(r'[a-zA-Z][a-zA-Z0-9+.-]*:')
SLUG_DROPPED = re.compile(r'[^\w\- ]')
TAG = re.compile(r'</?[a-zA-Z][^<>]*>')


class Problems(list):
    def add(self, path, line, message):
        self.append(f'{path}:{line}: {message}' if line else f'{path}: {message}')


# --- GitHub's rendering rules -------------------------------------------------

def plain_text(heading):
    """Return the text GitHub derives a heading's anchor from."""
    text = re.sub(r'!?\[([^\]]*)\]\([^)]*\)', r'\1', heading)
    text = CODE_SPAN.sub(lambda match: match.group(2), text)
    text = TAG.sub('', text)
    text = re.sub(r'(\*\*|__)(?=\S)(.+?)(?<=\S)\1', r'\2', text)
    text = re.sub(r'(?<![\w*])\*(?=\S)(.+?)(?<=\S)\*(?![\w*])', r'\1', text)
    text = re.sub(r'(?<![\w_])_(?=\S)(.+?)(?<=\S)_(?![\w_])', r'\1', text)
    return re.sub(r'\\([!-/:-@\[-`{-~])', r'\1', text)


def slug(text):
    """GitHub's anchor for a heading, before duplicates are numbered."""
    text = unicodedata.normalize('NFC', text).strip().lower()
    return SLUG_DROPPED.sub('', text).replace(' ', '-')


@dataclasses.dataclass
class Heading:
    line: int
    level: int
    anchor: str
    explicit: bool


def code_lines(lines):
    """Return the indexes of the lines inside fenced code blocks, fences included."""
    inside, fence = set(), None
    for index, line in enumerate(lines):
        match = FENCE.match(line)
        if fence is None:
            if match:
                fence = match.group(2)
                inside.add(index)
        else:
            inside.add(index)
            if match and match.group(2)[0] == fence[0] and len(match.group(2)) >= len(fence) and not match.group(3).strip():
                fence = None
    return inside


def body_start(lines):
    """Return the index of the first line after YAML front matter, if any."""
    if lines and lines[0].strip() == '---':
        for index in range(1, len(lines)):
            if lines[index].strip() == '---':
                return index + 1
    return 0


def headings(lines):
    """Return the ATX headings of a Markdown file with GitHub's anchors.

    GitHub numbers a repeated anchor -1, -2 and so on, where Python-Markdown
    writes _1, so the site cannot leave this to its generator.
    """
    found, used, code = [], {}, code_lines(lines)
    for index in range(body_start(lines), len(lines)):
        match = None if index in code else HEADING.match(lines[index])
        if not match:
            continue
        text = CLOSING_HASHES.sub('', match.group(2))
        explicit = EXPLICIT_ID.search(text)
        if explicit:
            anchor = explicit.group(1)
            used.setdefault(anchor, 0)
        else:
            anchor = base = slug(plain_text(text))
            while anchor in used:
                used[base] += 1
                anchor = f'{base}-{used[base]}'
            used[anchor] = 0
        found.append(Heading(index + 1, len(match.group(1)), anchor, bool(explicit)))
    return found


def reindent(lines):
    """Indent list content by four spaces for each level.

    GitHub nests a list or continues an item at the column where the item's
    text starts, two or three columns in. Python-Markdown wants four spaces for
    each level and otherwise flattens the list or ends it.
    """
    out, items, fence, previous_blank = [], [], None, True
    for line in lines:
        if not line.strip():
            out.append(line)
            previous_blank = True
            continue
        indent = len(line) - len(line.lstrip(' '))
        if fence is not None:
            # A fenced block moves as a whole with the line that opened it.
            out.append(' ' * max(0, indent + fence[1]) + line.lstrip(' '))
            match = FENCE.match(line)
            if match and match.group(2)[0] == fence[0][0] and len(match.group(2)) >= len(fence[0]) and not match.group(3).strip():
                fence = None
            previous_blank = False
            continue
        item = LIST_ITEM.match(line)
        if items and indent < items[-1] and not item and not previous_blank:
            # A lazy continuation: the paragraph above goes on, less indented.
            out.append(line)
            continue
        in_list = bool(items)
        while items and indent < items[-1]:
            items.pop()
        shifted = 4 * len(items) + indent - items[-1] if items else indent
        if item and not previous_blank and not in_list and out and not out[-1].lstrip().startswith(('#', '>', '|')) \
                and item.group(2) in ('-', '*', '+', '1.', '1)'):
            # GitHub lets a list interrupt a paragraph; Python-Markdown needs
            # the blank line.
            out.append('')
        out.append(' ' * shifted + line.lstrip(' '))
        match = FENCE.match(line)
        if match:
            fence = (match.group(2), shifted - indent)
        if item:
            spaces = len(item.group(3)) if len(item.group(3)) <= 4 else 1
            items.append(indent + len(item.group(2)) + spaces)
        previous_blank = False
    return out


# --- The repository and the site plan ----------------------------------------

def repository_files(root):
    """Return the files GitHub has or will have: tracked, or new and not ignored."""
    if (root / '.git').exists():
        try:
            output = subprocess.run(['git', '-C', str(root), 'ls-files', '-z', '--cached', '--others', '--exclude-standard'],
                                    check=True, capture_output=True).stdout
            return {name for name in output.decode('utf-8').split('\0') if name and (root / name).is_file()}
        except (OSError, subprocess.SubprocessError):
            pass
    # Not a checkout, such as an unpacked source archive.
    return {path.relative_to(root).as_posix() for path in root.rglob('*')
            if path.is_file() and '.git' not in path.relative_to(root).parts
            and not path.relative_to(root).as_posix().startswith(('website/build/', 'website/.cache/'))}


def directories(files):
    found = set()
    for name in files:
        while '/' in name:
            name = name.rsplit('/', 1)[0]
            found.add(name)
    return found


def page_url(page):
    """Return a page's address below the site root, as the generator publishes it."""
    directory, name = posixpath.split(page)
    if name in ('index.md', 'README.md'):
        return directory + '/' if directory else ''
    return page[:-len('.md')] + '/'


@dataclasses.dataclass
class Site:
    root: Path
    site_url: str
    repo_url: str
    files: set
    pages: dict = dataclasses.field(default_factory=dict)       # site path -> repository path
    assets: dict = dataclasses.field(default_factory=dict)      # site path -> repository path
    generated: dict = dataclasses.field(default_factory=dict)   # site path -> bytes
    sections: dict = dataclasses.field(default_factory=dict)    # (repository path, anchor) -> (page, anchor)
    problems: Problems = dataclasses.field(default_factory=Problems)
    counts: dict = dataclasses.field(default_factory=dict)
    cache: dict = dataclasses.field(default_factory=dict)

    def __post_init__(self):
        self.directories = directories(self.files)
        for name in sorted(self.files):
            if name.startswith(SITE_ONLY):
                self.place(name[len(SITE_ONLY):], name)
            elif name in ROOT_PAGES:
                self.place(ROOT_PAGES[name], name)
            elif name.startswith('docs/') and name.endswith('.md') and name.count('/') == 1:
                self.place(name[len('docs/'):], name)
            elif name.startswith(PUBLISHED):
                self.place(name[len('docs/'):], name)
        self.sources = {source: page for page, source in self.pages.items()}
        self.published = {source: path for path, source in self.assets.items()}
        for path, source in sorted(self.assets.items()):
            recipe = RECIPES.fullmatch(source)
            if recipe:
                self.hosted_recipe(recipe.group(1), source)

    def place(self, path, source):
        kind = self.pages if path.endswith('.md') else self.assets
        taken = self.pages.get(path) or self.assets.get(path)
        if taken:
            self.problems.add(source, 0, f'would be published as {path}, which {taken} already is.')
        else:
            kind[path] = source

    def hosted_recipe(self, name, source):
        text = self.text(source)
        hosted = text.replace(f'"{LOCAL_DEMO}', f'"{self.site_url}{DEMO}')
        if hosted == text or '127.0.0.1' in hosted:
            self.problems.add(source, 0, f'expected every feed address to start with {LOCAL_DEMO}, which the site '
                              'replaces with the published demo.')
        self.generated[HOSTED + name] = hosted.encode('utf-8')

    def text(self, source):
        return (self.root / source).read_text(encoding='utf-8')

    def lines(self, source):
        if source not in self.cache:
            lines = self.text(source).split('\n')
            self.cache[source] = (lines, headings(lines))
        return self.cache[source]

    def anchors(self, source):
        return {heading.anchor for heading in self.lines(source)[1]}

    def count(self, kind, *link):
        # A section the site includes is read twice; its links count once.
        self.counts.setdefault(kind, set()).add(link)

    def urls(self):
        """Return every address below the site root that the build publishes."""
        return {page_url(page) for page in self.pages} | set(self.assets) | set(self.generated)


def load_site(root):
    with open(root / CONFIG, 'rb') as file:
        project = tomllib.load(file)['project']
    site_url = project['site_url'].rstrip('/') + '/'
    return Site(root, site_url, project['repo_url'].rstrip('/'), repository_files(root)), project


# --- Links --------------------------------------------------------------------

def relative(target, page):
    return posixpath.relpath(target, posixpath.dirname(page) or '.')


def resolve(site, origin, line, destination, page):
    """Return where a link in origin must point once its text is on page.

    The link is resolved as GitHub resolves it, against the file it was written
    in. None means the link stays as written.
    """
    if destination.startswith('<') and destination.endswith('>'):
        destination = destination[1:-1]
    if not destination or destination.startswith('//'):
        return None
    if SCHEME.match(destination):
        check_absolute(site, origin, line, destination)
        return None
    path, _, fragment = destination.partition('#')
    path, _, query = path.partition('?')
    path = urllib.parse.unquote(path)
    if not path:
        target = origin
    elif path.startswith('/'):
        target = posixpath.normpath(path.lstrip('/'))
    else:
        target = posixpath.normpath(posixpath.join(posixpath.dirname(origin), path))
    if target == '.':
        target = ''
    if target.startswith('..'):
        site.problems.add(origin, line, f'link {destination} leaves the repository.')
        return None
    if target in site.directories or target == '':
        return directory_link(site, origin, line, destination, target, fragment, page)
    if target not in site.files:
        site.problems.add(origin, line, f'link {destination} points to {target}, which the repository does not have.')
        return None
    suffix = '#' + fragment if fragment else ''
    if target.endswith('.md'):
        if fragment and fragment not in site.anchors(target):
            site.problems.add(origin, line, f'link {destination} names an anchor that {target} does not have.')
            return None
        if fragment:
            site.count('anchors', origin, line, destination)
        if (target, fragment) in site.sections:
            section_page, anchor = site.sections[target, fragment]
            site.count('pages', origin, line, destination)
            return ('' if section_page == page else relative(section_page, page)) + '#' + anchor
        if target in site.sources:
            site.count('pages', origin, line, destination)
            if site.sources[target] == page and (not path or fragment):
                return suffix or None
            return relative(site.sources[target], page) + suffix
    if target in site.published:
        site.count('files', origin, line, destination)
        return relative(site.published[target], page) + ('?' + query if query else '') + suffix
    site.count('repository', origin, line, destination)
    return f'{site.repo_url}/blob/{BRANCH}/{urllib.parse.quote(target, safe="/@+~,")}{suffix}'


def directory_link(site, origin, line, destination, target, fragment, page):
    suffix = '#' + fragment if fragment else ''
    readme = posixpath.join(target, 'README.md')
    if readme in site.sources:
        if fragment and fragment not in site.anchors(readme):
            site.problems.add(origin, line, f'link {destination} names an anchor that {readme} does not have.')
            return None
        site.count('pages', origin, line, destination)
        return relative(site.sources[readme], page) + suffix
    index = posixpath.join(target, 'index.html')
    if index in site.published:
        site.count('files', origin, line, destination)
        return relative(site.published[index], page) + suffix
    site.count('repository', origin, line, destination)
    return f'{site.repo_url}/tree/{BRANCH}/{urllib.parse.quote(target, safe="/@+~,")}'.rstrip('/') + suffix


def check_absolute(site, origin, line, destination):
    """Check an absolute link that points back into the site or the repository."""
    address = destination.partition('#')[0].partition('?')[0]
    if address.startswith(site.site_url) or address + '/' == site.site_url:
        path = urllib.parse.unquote(address[len(site.site_url):])
        if path not in site.urls() and path + '/' not in site.urls():
            site.problems.add(origin, line, f'link {destination} points to a page or file the site does not publish.')
        site.count('site', origin, line, destination)
        return
    for kind in ('blob', 'tree'):
        prefix = f'{site.repo_url}/{kind}/{BRANCH}/'
        if address.startswith(prefix):
            target = urllib.parse.unquote(address[len(prefix):]).rstrip('/')
            if target not in (site.files if kind == 'blob' else site.directories):
                site.problems.add(origin, line, f'link {destination} points to {target}, which the repository does not have.')
            site.count('repository', origin, line, destination)
            return
    site.count('external', origin, line, destination)


def mask_code(line):
    """Blank out code spans, keeping every other character in its column."""
    return CODE_SPAN.sub(lambda match: ' ' * len(match.group(0)), line)


def rewrite_links(site, origin, number, line, page):
    """Return the line with every link pointing to the right place for page."""
    masked = mask_code(line)
    spans = []
    for match in INLINE_LINK.finditer(masked):
        spans.append(match.span(1))
    reference = REFERENCE.match(masked)
    if reference:
        spans.append(reference.span(1))
    for match in HTML_ATTRIBUTE.finditer(masked):
        spans.append(match.span(1))
    for match in AUTOLINK.finditer(masked):
        check_absolute(site, origin, number, match.group(1))
    for start, end in sorted(set(spans), reverse=True):
        target = resolve(site, origin, number, line[start:end], page)
        if target is not None:
            line = line[:start] + target + line[end:]
    return line


# --- Staging ------------------------------------------------------------------

def render(site, origin, page):
    """Return origin's lines as page needs them, one list of lines per source line."""
    lines, found = site.lines(origin)
    anchors = {heading.line: heading for heading in found}
    code = code_lines(lines)
    rendered = []
    for index, line in enumerate(lines):
        if index not in code and index >= body_start(lines):
            line = rewrite_links(site, origin, index + 1, line, page)
            heading = anchors.get(index + 1)
            if heading and not heading.explicit:
                line = f'{line.rstrip()} {{ #{heading.anchor} }}'
            if line.strip() == '<details>':
                # GitHub renders Markdown inside the block; the site's Markdown
                # needs to be told.
                line = '<details markdown="1">'
        rendered.append(line)
    return rendered


def section(site, origin, anchor):
    """Return the line range of a heading's section: [start, end) as indexes."""
    lines, found = site.lines(origin)
    for position, heading in enumerate(found):
        if heading.anchor == anchor:
            end = len(lines)
            for later in found[position + 1:]:
                if later.level <= heading.level:
                    end = later.line - 1
                    break
            return heading.line - 1, end
    return None


def includes(site, origin):
    """Return the sections a site-only page includes: {line index: (source, start, end)}."""
    lines, _ = site.lines(origin)
    found, code = {}, code_lines(lines)
    for index, line in enumerate(lines):
        match = None if index in code else INCLUDE.match(line.strip())
        if not match:
            continue
        source = posixpath.normpath(match.group(1))
        if source not in site.files or not source.endswith('.md'):
            site.problems.add(origin, index + 1, f'include names {source}, which is not a Markdown file in the repository.')
            continue
        span = section(site, source, match.group(2))
        if span is None:
            site.problems.add(origin, index + 1, f'include names a section that {source} does not have: #{match.group(2)}.')
            continue
        found[index] = (source, *span)
    return found


def register_sections(site):
    """Record where included sections are published, so links to them follow."""
    for page, origin in sorted(site.pages.items()):
        if not origin.startswith(SITE_ONLY):
            continue
        own = site.anchors(origin)
        for index, (source, start, end) in sorted(includes(site, origin).items()):
            for heading in site.lines(source)[1]:
                if not start < heading.line <= end:
                    continue
                key = (source, heading.anchor)
                if heading.anchor in own or key in site.sections:
                    site.problems.add(origin, index + 1, f'the included heading #{heading.anchor} is on the site twice.')
                site.sections[key] = (page, heading.anchor)
                own = own | {heading.anchor}


def front_matter(lines, origin):
    """Split a page into its front matter, with the source path added, and its body."""
    start = body_start(lines)
    meta = [line for line in lines[1:max(start - 1, 0)] if line.strip()]
    if not any(line.startswith(SOURCE_KEY + ':') for line in meta):
        meta.append(f'{SOURCE_KEY}: {origin}')
    return ['---', *meta, '---'], start


def stage_page(site, page, origin):
    rendered = render(site, origin, page)
    meta, start = front_matter(site.lines(origin)[0], origin)
    body = []
    included = includes(site, origin) if origin.startswith(SITE_ONLY) else {}
    for index in range(start, len(rendered)):
        if index in included:
            source, first, last = included[index]
            body.extend(render(site, source, page)[first:last])
        else:
            body.append(rendered[index])
    return ('\n'.join(meta + reindent(body)).rstrip('\n') + '\n').encode('utf-8')


def stage(site):
    """Return every file of the staged site: {site path: bytes}."""
    register_sections(site)
    staged = dict(site.generated)
    for path, source in site.assets.items():
        staged[path] = (site.root / source).read_bytes()
    for page, origin in sorted(site.pages.items()):
        staged[page] = stage_page(site, page, origin)
    return staged


def check_other_guides(site):
    """Check Markdown that GitHub shows but the site does not publish."""
    for name in sorted(site.files):
        shown = '/' not in name or name.startswith('docs/') and posixpath.basename(name) == 'README.md'
        if shown and name.endswith('.md') and name not in site.sources:
            render(site, name, name)


def nav_pages(nav):
    for entry in nav:
        if isinstance(entry, str):
            yield entry
        elif isinstance(entry, dict):
            for value in entry.values():
                yield from nav_pages(value if isinstance(value, list) else [value])


def check_nav(site, project):
    listed = [entry for entry in nav_pages(project.get('nav', [])) if not SCHEME.match(entry)]
    for entry in listed:
        if entry not in site.pages:
            site.problems.add(CONFIG, 0, f'nav lists {entry}, which is not a page of the site.')
    for page, origin in sorted(site.pages.items()):
        if page not in listed:
            site.problems.add(CONFIG, 0, f'nav does not list {page} ({origin}), so readers could not find it.')


def write(directory, staged):
    """Make directory hold exactly the staged files, touching only what changed."""
    directory.mkdir(parents=True, exist_ok=True)
    changed = 0
    for path, content in staged.items():
        target = directory / path
        if not target.is_file() or target.read_bytes() != content:
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(content)
            changed += 1
    for target in sorted(directory.rglob('*'), reverse=True):
        if target.is_file() and target.relative_to(directory).as_posix() not in staged:
            target.unlink()
            changed += 1
        elif target.is_dir() and not any(target.iterdir()):
            target.rmdir()
    return changed


def prepare(root):
    site, project = load_site(root)
    staged = stage(site)
    check_other_guides(site)
    check_nav(site, project)
    return site, project, staged


def summary(site):
    counts = {kind: len(links) for kind, links in site.counts.items()}
    return (f'{len(site.pages)} pages, {len(site.assets) + len(site.generated)} files; links: '
            f'{counts.get("pages", 0)} to pages, {counts.get("files", 0)} to published files, '
            f'{counts.get("repository", 0)} to repository files, {counts.get("site", 0)} to the site by address, '
            f'{counts.get("external", 0)} external (not fetched); {counts.get("anchors", 0)} anchors checked')


# --- The built site -----------------------------------------------------------

LEAKS = (
    (re.compile(r'(^|\n)\s*([-*+]|\d+[.)]) \S'), 'a list item'),
    (re.compile(r'```|~~~'), 'a code fence'),
    (re.compile(r'\]\([^)\s]+\)'), 'a link'),
    (re.compile(r'\*\*\S|\S\*\*'), 'bold markers'),
    (re.compile(r'\{ ?#[\w-]+ ?\}'), 'a heading anchor'),
    (re.compile(r'\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]'), 'an alert marker'),
    (re.compile(r'(^|\n)\s*#{1,6} \S'), 'a heading'),
    (re.compile(r'(^|\n)\s*\|.*\|\s*(\n|$)'), 'a table row'),
)
# What a browser fetches without being asked. A page that names another origin
# here contacts it for every reader.
LOADED = {('script', 'src'), ('img', 'src'), ('img', 'srcset'), ('source', 'src'), ('source', 'srcset'),
          ('iframe', 'src'), ('video', 'src'), ('audio', 'src'), ('embed', 'src'), ('object', 'data'),
          ('link', 'href')}
PASSIVE_LINKS = {'canonical', 'alternate', 'prev', 'next', 'edit', 'describedby'}


class Page(html.parser.HTMLParser):
    """Collect what a built page links to, loads and can be linked at."""

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.ids, self.links, self.loads, self.text = set(), [], [], []
        self.article = self.code = 0

    def handle_starttag(self, tag, attributes):
        attributes = dict(attributes)
        if attributes.get('id'):
            self.ids.add(attributes['id'])
        if tag == 'a' and attributes.get('name'):
            self.ids.add(attributes['name'])
        if tag == 'a' and attributes.get('href') is not None:
            self.links.append(attributes['href'])
        for (element, name) in LOADED:
            if tag == element and attributes.get(name):
                if tag == 'link' and not (set((attributes.get('rel') or '').lower().split()) - PASSIVE_LINKS):
                    continue
                values = [attributes[name]]
                if name == 'srcset':
                    values = [part.split()[0] for part in attributes[name].split(',') if part.split()]
                self.loads.extend(values)
        if tag == 'article':
            self.article += 1
        elif self.article and tag in ('pre', 'code', 'script', 'style', 'svg'):
            self.code += 1
        elif self.article and tag in ('p', 'li', 'td', 'th', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'summary'):
            self.text.append('')

    def handle_endtag(self, tag):
        if tag == 'article':
            self.article -= 1
        elif self.article and tag in ('pre', 'code', 'script', 'style', 'svg'):
            self.code -= 1

    def handle_data(self, data):
        if self.article and not self.code and self.text:
            self.text[-1] += data


def verify(root, site, project, staged):
    """Check the built site and return a summary of what was checked."""
    problems = site.problems
    output = root / posixpath.dirname(CONFIG) / project.get('site_dir', 'site')
    if not (output / 'index.html').is_file():
        problems.add(output.relative_to(root).as_posix(), 0, 'has no index.html; build the site first (make docs).')
        return ''
    pages = {}
    for path in sorted(output.rglob('*.html')):
        name = path.relative_to(output).as_posix()
        if name.startswith(DEMO):
            continue
        parser = Page()
        parser.feed(path.read_text(encoding='utf-8'))
        pages[name] = parser
    counts = dict.fromkeys(('links', 'anchors', 'loads', 'repository', 'edit', 'external', 'expected'), 0)
    base = urllib.parse.urlsplit(site.site_url).path

    def target_of(name, reference):
        """Return the built file a relative reference on a page points to."""
        path = urllib.parse.unquote(reference.partition('#')[0].partition('?')[0])
        if reference.startswith(site.site_url):
            target = urllib.parse.unquote(reference[len(site.site_url):].partition('#')[0].partition('?')[0])
        elif path.startswith(base):
            # The 404 page is served at any depth, so it links from the root.
            target = path[len(base):]
        elif path.startswith('/'):
            return None
        else:
            target = posixpath.normpath(posixpath.join(posixpath.dirname(name), path)) if path else name
        if target in ('.', ''):
            target = 'index.html'
        if target.startswith('..'):
            return None
        if (output / target).is_dir():
            target = posixpath.join(target, 'index.html')
        return target

    for name, parser in pages.items():
        for reference in parser.loads:
            counts['loads'] += 1
            if SCHEME.match(reference) and not reference.startswith('data:') or reference.startswith('//'):
                problems.add(name, 0, f'loads {reference} from another site when viewed.')
            elif not reference.startswith('data:'):
                target = target_of(name, reference)
                if target is None or not (output / target).is_file():
                    problems.add(name, 0, f'loads {reference}, which the built site does not have.')
        for reference in parser.links:
            fragment = urllib.parse.unquote(reference.partition('#')[2])
            if SCHEME.match(reference) and not reference.startswith(site.site_url):
                repository = re.match(re.escape(site.repo_url) + r'/(blob|tree|edit|raw)/' + BRANCH + r'/([^#?]*)', reference)
                if repository:
                    counts['edit' if repository.group(1) == 'edit' else 'repository'] += 1
                    target = urllib.parse.unquote(repository.group(2)).rstrip('/')
                    if target not in (site.directories if repository.group(1) == 'tree' else site.files):
                        problems.add(name, 0, f'links to {reference}, which the repository does not have.')
                else:
                    counts['external'] += 1
                continue
            counts['links'] += 1
            target = target_of(name, reference)
            if target is None or not (output / target).is_file():
                problems.add(name, 0, f'links to {reference}, which the built site does not have.')
            elif fragment and target in pages:
                counts['anchors'] += 1
                if fragment not in pages[target].ids:
                    problems.add(name, 0, f'links to {reference}, but that page has no such anchor.')
        if name != '404.html':
            for text in parser.text:
                for pattern, what in LEAKS:
                    if pattern.search(text):
                        problems.add(name, 0, f'shows Markdown as text ({what}): {" ".join(text.split())[:90]!r}')
                        break
    for page, origin in sorted(site.pages.items()):
        built = posixpath.join(page_url(page), 'index.html')
        if built not in pages:
            problems.add(origin, 0, f'was staged as {page} but the build has no {built}.')
            continue
        expected = site.anchors(origin) | {anchor for (_, _), (target, anchor) in site.sections.items() if target == page}
        counts['expected'] += len(expected)
        for anchor in sorted(expected - pages[built].ids):
            problems.add(origin, 0, f'GitHub gives a heading the anchor #{anchor}, which {built} does not have.')
    for path, content in staged.items():
        if not path.endswith('.md') and (not (output / path).is_file() or (output / path).read_bytes() != content):
            problems.add(output.relative_to(root).as_posix() + '/' + path, 0, 'is missing or differs from its source.')
    return (f'{len(pages)} built pages; {counts["links"]} internal links ({counts["anchors"]} with an anchor), '
            f'{counts["expected"]} GitHub heading anchors present, {counts["loads"]} images, styles and scripts '
            f'(none from another site), {counts["repository"]} links to repository files, {counts["edit"]} edit links, '
            f'{counts["external"]} external links (not fetched)')


# --- Commands -----------------------------------------------------------------

def report(site, what='The guides have broken links or cannot be staged'):
    if not site.problems:
        return 0
    print(what + ':', file=sys.stderr)
    for problem in dict.fromkeys(site.problems):
        print('- ' + problem, file=sys.stderr)
    return 1


def watched(root):
    state = {}
    for base in ('docs', 'website'):
        for directory, names, files in os.walk(root / base):
            if os.path.basename(directory) == 'build' and Path(directory).parent == root / 'website':
                names[:] = []
                continue
            for name in files:
                status = os.stat(os.path.join(directory, name))
                state[os.path.join(directory, name)] = (status.st_mtime_ns, status.st_size)
    for path in root.glob('*.md'):
        status = path.stat()
        state[str(path)] = (status.st_mtime_ns, status.st_size)
    return state


def serve(root, source, command):
    def stop(*_):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, stop)
    process = subprocess.Popen(command)
    try:
        state = watched(root)
        while process.poll() is None:
            time.sleep(1)
            current = watched(root)
            if current == state:
                continue
            state = current
            site, _, staged = prepare(root)
            # A half-written link should not take the preview down; report it
            # and show the rest.
            report(site)
            print(f'Staged {write(source, staged)} changed files.', flush=True)
    except KeyboardInterrupt:
        pass
    finally:
        process.terminate()
        process.wait()
    return process.returncode or 0


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split('\n\n')[0])
    parser.add_argument('command', choices=('check', 'stage', 'verify', 'serve'))
    parser.add_argument('--root', type=Path, default=ROOT, help='repository root')
    parser.add_argument('--zensical', default='zensical', help='zensical command, for serve')
    parser.add_argument('--address', default='127.0.0.1:8090', help='address the preview listens on, for serve')
    args = parser.parse_args(argv)
    root = args.root.resolve()
    site, project, staged = prepare(root)
    source = root / posixpath.dirname(CONFIG) / project['docs_dir']
    if args.command == 'verify':
        checked = verify(root, site, project, staged)
        if report(site, 'The built site has problems'):
            return 1
        print('Built site checked: ' + checked + '.')
        return 0
    if report(site):
        return 1
    if args.command == 'check':
        print('Guides checked: ' + summary(site) + '.')
        return 0
    changed = write(source, staged)
    print(f'Staged {len(staged)} files in {source.relative_to(root).as_posix()} ({changed} changed): {summary(site)}.',
          flush=True)
    if args.command == 'serve':
        return serve(root, source, [args.zensical, 'serve', '--config-file', str(root / CONFIG), '--dev-addr', args.address])
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        print(f'docs-site: {error}', file=sys.stderr)
        sys.exit(1)
