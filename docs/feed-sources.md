# RSS and Atom feed sources

A site that already publishes an RSS or Atom feed needs no selectors. A **feed source** reads that feed's entries as its stories, and everything after extraction works as it does for a web page: [story filters](filtering.md) and [library filters](filtering.md#library-filters), [full article content](atom.md#full-article-content), date settings, the feed's own User-Agent and Accept-Language, [diagnostics](operations.md#feed-diagnostics), and the private RSS and Atom links. Use one to drop sponsored or off-topic posts from a busy feed, to publish the whole article from a feed that carries only summaries, or to give a YouTube channel or a Mastodon account one private link with your own rules.

## Set one up

1. Choose **New feed**, then **Source → RSS or Atom feed**. The selectors, the visual selector, the fetch mode and the render settings disappear: the feed supplies every field.
2. Enter the address of the feed itself as **Feed URL**, such as `https://example.com/feed.xml`, not the site's home page. Pages that only link to their feed are not followed.
3. Add story filters, full article content or date settings if you need them.
4. Choose **Preview items**, then save.

A saved feed keeps its source: **Edit** shows it locked. A feed and a web page identify stories differently, so switching would publish every saved story a second time. To try the other source, **Duplicate** the feed and choose it in the copy.

The [demo site](examples/) publishes its own `feed.xml`, and `demo-feed-recipe.json` reads it, filters out the sponsored story and fetches each article.

## What is read

RSS 0.9x and 2.0, RSS 1.0 (RDF) and Atom are read. JSON Feed is not.

| Story field | Read from |
| --- | --- |
| Title | The entry's title, as text. An RSS title is read as markup only when it holds unescaped elements, or when it closes a tag and every tag is a common inline element such as `<b>` or `<em>`; otherwise a `<` in it is text, as in `std::vector<bool>`. An Atom title follows its `type`. An entry without one, as Mastodon and other microblog feeds publish, takes the first 100 characters of its description's text. |
| Link | RSS `link`, else a `guid` that is a permalink web address. Atom's first `alternate` link, else an `id` that is a web address. Relative links resolve against `xml:base` and the feed's own address. |
| Description | `content:encoded` or Atom `content`, else `description` or `summary`, else `media:description`. Markup is sanitized and made absolute as a web page's description is. A description over 256 KiB falls back to the summary, and so does one that shows nothing once sanitized, such as a post that is only a video embed or a tracking image. |
| Image | The first image the entry declares: `media:thumbnail`, `media:content` that is an image, the same inside `media:group`, an image enclosure, or `itunes:image`. Otherwise the first image in the description that is not 0 or 1 pixel wide or high, as tracking images are. As for web pages, the image is shown at the top of the description unless the description already shows it. |
| Date | `pubDate`, `published` or `issued`, then `dc:date`, then `updated` or `modified`, with the recipe's Go date layout and time zone. See [publication dates](dates.md#feed-sources). |

[Story filters](filtering.md) read the entry's own description, chosen as above: the full post when the entry carries one in `content:encoded` or Atom `content`, and the summary otherwise. As for a web page, they never read a fetched [full article](atom.md#full-article-content), so an exclude rule for a word that appears only on the article page keeps the story.

## Story identity

A story is identified by its `guid`, Atom `id` or `rdf:about`, else by its link, else by its title, which is what feed readers do. Entries that share one link therefore stay separate stories. A story with a `guid`, `id` or `rdf:about` keeps its identity, its GUID in your feed and its publication date when its source edits its text, link or update time. One identified by its link gets a new identity if the link changes, and one identified by its title if the title changes: readers then see it again as a new story. A source that changes its identifiers, for example after moving to new software, publishes every story again under new GUIDs until retention removes the old ones; feed readers subscribed to the source directly see the same.

## Fetching

A feed source is fetched as Static HTTP only, through the same guarded fetcher and [outbound policy](deployment.md) as web pages. Its request asks for RSS, Atom or XML in its `Accept` header, and its encoding comes from a byte order mark, then the `Content-Type` charset, then the XML declaration, so a Latin-1 feed that names its encoding only there reads correctly. The feed's User-Agent and Accept-Language are sent as for a web page, and the source's `ETag` and `Last-Modified` make later requests conditional: a source that answers `304 Not Modified` publishes nothing new, and readers keep receiving 304 too.

Article pages are web pages: they are requested as HTML and fetched as plain HTML, never rendered in Chromium, so a feed source never waits for a Chromium or FlareSolverr slot. Their selector is CSS, such as `.article-body`; XPath is refused.

## Limits and failures

- A feed is limited to 4 MiB, as received and once decoded, like a web page. Long podcast feeds with full show notes can exceed it.
- The first 1,000 entries are read, with a warning, since feeds list their newest first. A feed that lists more, oldest first, would show only its oldest.
- `MAX_ITEMS` (500 by default) is retention over everything stored. A feed that lists more entries than that stores and removes the oldest again on every refresh, and full article fetching can spend its 10 articles per refresh on them, so keep `MAX_ITEMS` above the number of entries such a feed lists.
- A document that cannot be read as XML fails the refresh with `feed is not well-formed XML (line N)`: one malformed entry fails the whole feed. Saved stories are kept and the feed backs off, as after any failed refresh. Errors name a line, never text from the feed, because they reach logs and notifications.
- Characters XML does not allow, such as a control character pasted into a title, are removed rather than failing the feed, and bytes that are not UTF-8 become `�`.
- An HTML page or a JSON Feed fails with `source is not an RSS or Atom feed`.
- Anything after the feed's root element, such as a notice or a script a server printed after the feed, is ignored.
- A well-formed feed with no entries is a successful refresh: saved stories are kept and nothing backs off, and Diagnostics notes that the feed lists no entries. If it stays empty, the library marks the feed [Quiet](operations.md#quiet-feeds).
- An entry is skipped, with a warning in the preview and Diagnostics, when its link is not a web address, when it has neither a title nor description text, when it repeats an earlier entry's identity, or when it is too large to store as a story. A feed whose entries are all skipped fails like a page with no valid stories.

## Published output

A feed source publishes RSS and Atom as any other feed does. The channel `link` in RSS, the `alternate` link in Atom and the `htmlUrl` in the OPML export are the address you entered, which here is the source feed rather than a web page. A source whose entries change on every request, such as a comment count or a tracking image with a random address, changes your feed on every refresh too.

## Recipe files

A feed source's recipe has `"type": "feed"` and no item or field selectors, in the same [version-1 recipe format](recipe-portability.md). Releases before 1.3 refuse such a file with `selector type must be css or xpath`.
