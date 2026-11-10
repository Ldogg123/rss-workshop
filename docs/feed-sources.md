# RSS and Atom feed sources

A site that already publishes an RSS or Atom feed needs no selectors. A **feed source** reads that feed's entries as its stories, and everything after extraction works as it does for a web page: [story filters](filtering.md) and [library filters](filtering.md#library-filters), [full article content](atom.md#full-article-content), [content cleanup](#content-cleanup), date settings, the feed's own User-Agent and Accept-Language, [diagnostics](operations.md#feed-diagnostics), and the private RSS and Atom links. Use one to drop sponsored or off-topic posts from a busy feed, to publish the whole article from a feed that carries only summaries, or to give a YouTube channel or a Mastodon account one private link with your own rules.

## Set one up

1. Choose **New feed**, then **Source → RSS or Atom feed**. The selectors, the visual selector, the fetch mode and the render settings disappear: the feed supplies every field.
2. Enter the address of the feed itself as **Feed URL**, such as `https://example.com/feed.xml`, not the site's home page. Pages that only link to their feed are not followed.
3. Add story filters, full article content, content cleanup or date settings if you need them.
4. Choose **Preview items**, then save.

A saved feed keeps its source: **Edit** shows it locked. A feed and a web page identify stories differently, so switching would publish every saved story a second time. To try the other source, **Duplicate** the feed and choose it in the copy.

The [demo site](examples/) publishes its own `feed.xml`, and `demo-feed-recipe.json` reads it, filters out the sponsored story and fetches each article.

## What is read

RSS 0.9x and 2.0, RSS 1.0 (RDF) and Atom are read. JSON Feed is not.

| Story field | Read from |
| --- | --- |
| Title | The entry's title, as text. An RSS title is read as markup only when it holds unescaped elements, or when it closes a tag and every tag is a common inline element such as `<b>` or `<em>`; otherwise a `<` in it is text, as in `std::vector<bool>`. An Atom title follows its `type`. An entry without one, as Mastodon and other microblog feeds publish, takes the first 100 characters of its description's text, read before [cleanup](#content-cleanup). |
| Link | RSS `link`, else a `guid` that is a permalink web address. Atom's first `alternate` link, else an `id` that is a web address. Relative links resolve against `xml:base` and the feed's own address. |
| Description | `content:encoded` or Atom `content`, else `description` or `summary`, else `media:description`. Markup is sanitized and made absolute as a web page's description is. A description over 256 KiB falls back to the summary, and so does one that shows nothing once sanitized, such as a post that is only a video embed or a tracking image, or once [cleaned](#content-cleanup). |
| Image | The first image the entry declares: `media:thumbnail`, `media:content` that is an image, the same inside `media:group`, an image enclosure, or `itunes:image`. Otherwise the first image left in the description after [cleanup](#content-cleanup) that is not 0 or 1 pixel wide or high, as tracking images are. As for web pages, the image is shown at the top of the description unless the description already shows it. |
| Date | `pubDate`, `published` or `issued`, then `dc:date`, then `updated` or `modified`, with the recipe's Go date layout and time zone. See [publication dates](dates.md#feed-sources). |

[Story filters](filtering.md) read the entry's own description, chosen as above: the full post when the entry carries one in `content:encoded` or Atom `content`, and the summary otherwise. As for a web page, they never read a fetched [full article](atom.md#full-article-content), so an exclude rule for a word that appears only on the article page keeps the story.

## Content cleanup

[Content cleanup](atom.md#content-cleanup) removes share links, signatures, "appeared first on" lines and similar clutter from a feed source's descriptions and fetched articles as it does for a web page. Its rules are CSS, such as `.share` or `p:containsOwn("appeared first on")`, like the article selector; a rule written as XPath is refused with a message that says so.

Rules apply to each description in the order above before it is chosen, because a feed's full post and its summary are alternatives rather than one block:

- A full post that the rules leave with nothing to show, no text and no image other than a 0- or 1-pixel one, gives way to the summary, as a post that is only a video embed does. A rule that removes a boilerplate full post therefore publishes the entry's summary.
- Only when the rules empty every description that showed something is the description dropped, with the warning `cleanup removed the whole description`, as on a web page.
- A feed source has no Image selector, so the image in its description is the first one the rules leave: a rule can keep an avatar or a banner from becoming the story's image. Images the entry declares, such as `media:thumbnail`, come first as always and are not affected.
- The title of an untitled entry is read before cleanup, so rules never change a story's title or [identity](#story-identity), and never leave an entry to be skipped for want of a title.

[Story filters](filtering.md) read the cleaned description, and [full article content](atom.md#full-article-content) is cleaned with the same rules. **Preview items** shows the result a refresh stores.

## Story identity

A story is identified by its `guid`, Atom `id` or `rdf:about`, else by its link, else by its title, which is what feed readers do. Entries that share one link therefore stay separate stories. A story with a `guid`, `id` or `rdf:about` keeps its identity, its GUID in your feed and its publication date when its source edits its text, link or update time. One identified by its link gets a new identity if the link changes, and one identified by its title if the title changes: readers then see it again as a new story. A source that changes its identifiers, for example after moving to new software, publishes every story again under new GUIDs until retention removes the old ones; feed readers subscribed to the source directly see the same.

For [Quiet](operations.md#quiet-feeds), a story is new when an entry with an identity the feed has not stored is first stored, as for any feed. An entry whose text or link changes under the same `guid` or `id` is a changed story, which readers receive but which does not count as new.

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
- A well-formed feed with no entries is a successful refresh: saved stories are kept and nothing backs off, and Diagnostics notes that the feed lists no entries. Because it lists none of the saved stories, changing the [cleanup rules](#content-cleanup) then keeps their fetched articles. If it stays empty, the library marks the feed [Quiet](operations.md#quiet-feeds) once its threshold has passed without a new story: counted from the last new story it stored, or, for a feed that has never stored one, from its first successful refresh or the latest change to its title, address or refresh interval.
- An entry is skipped, with a warning in the preview and Diagnostics, when its link is not a web address, when it has neither a title nor description text, when it repeats an earlier entry's identity, or when it is too large to store as a story. A feed whose entries are all skipped fails like a page with no valid stories.

## Published output

A feed source publishes RSS and Atom as any other feed does. The channel `link` in RSS and the `alternate` link in Atom are the address you entered, which here is the source feed rather than a web page, so a reader that takes a feed's website link from the feed itself opens the source feed. The [OPML export](atom.md#subscribe-to-every-feed-at-once) leaves `htmlUrl` out for a feed source rather than offer that address as its website; its `xmlUrl` is your reader link, as for any feed. A source whose entries change on every request, such as a comment count or a tracking image with a random address, changes your feed on every refresh too.

## Recipe files

A feed source's recipe has `"type": "feed"` and no item or field selectors, in the same [version-1 recipe format](recipe-portability.md). Releases before 1.3 refuse such a file with `selector type must be css or xpath`.
