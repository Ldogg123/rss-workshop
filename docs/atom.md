# RSS and Atom readers

Each saved feed provides both **Open RSS feed** and **Open Atom feed**, with copy buttons beside them. Both formats read the same persisted items and never trigger scraping. Their URLs end in `.xml` and `.atom` respectively. Choose whichever your reader supports.

The formats share one private read token. **Reset feed links** revokes both old URLs. The replacement Atom self link changes, while its feed ID and item IDs remain stable. A new recipe import creates a separate feed identity and separate reader URLs.

Atom responses use `application/atom+xml; charset=utf-8`, the Atom 1.0 namespace, absolute alternate/self links, escaped HTML content, and content-derived ETags. Conditional requests support `If-None-Match`, including weak tags. RSS and Atom have separate ETags. New feeds return HTTP 503 with `Retry-After: 60` until their first successful refresh; failed or empty extraction retains the last successful content.

Atom entry IDs use the same stored GUIDs as RSS. Publication dates remain the saved publication/first-seen values. Entry `updated` uses the last successful observation because the current database does not store a separate per-item content-change timestamp; feed `updated` uses the latest successful refresh or entry timestamp. These are stored timestamps, so reader requests alone never change output bytes. The feed title is used as the fallback feed author, since recipes do not yet extract authors.

Reader responses do not support Last-Modified handling. Recipe files are covered by the [export/import guide](recipe-portability.md). The Atom representation follows [RFC 4287](https://www.rfc-editor.org/rfc/rfc4287).

## Full article content

By default a story's description is whatever the list page shows, which is often a short teaser. **Full article content** in the feed editor follows each story's own link, extracts the article body from that page with a CSS or XPath selector, and publishes that instead. The selector runs against the article page, so XPath here is absolute (`//div[@class="article-body"]`) rather than relative like the per-field selectors.

Article pages are fetched as plain HTML through the same guarded fetcher as list pages, with the same destination checks, redirect validation and size limits. Rendering them with Chromium or FlareSolverr is an explicit per-feed option, because a feed has many article pages and the browser pool is shared with every other feed.

Each refresh fetches at most 10 article pages, so a long feed fills in over several refreshes rather than in one burst; nothing is skipped permanently. A story is fetched once and then left alone, except within the first hour after it is discovered, when it is re-fetched so a source that finishes an article shortly after listing it is picked up. An article that cannot be fetched or whose selector matches nothing keeps the list-page description and records a warning in diagnostics; it never fails the refresh or removes the story.

Fetched bodies are stored separately from the list-page description. Re-extracting the list page every refresh therefore keeps updating a preview image published after the story went live, without discarding an article body already retrieved. The current image is published with the article body, so a late image still reaches the reader.

**Preview items** fetches the first 3 articles only. It runs while you wait and must not fan out a request for every story on the page, so a preview shows that the selector works rather than building the whole feed. Those previewed stories display the fetched article body, labelled, so a selector that matches the wrong block — a navigation bar, a sidebar, a cookie notice — is visible before you save.

Changing the article selector or its attribute, or clearing it to turn the feature off, discards the article bodies already stored for that feed in the same save. Later refreshes fetch them again with the new selector, or leave the list-page description in place. Nothing else about the stories changes: their identity, publication dates and list-page descriptions are untouched, so readers see no duplicates.

## Subscribe to every feed at once

**Export OPML** downloads `rss-workshop.opml`, an [OPML 2.0](http://opml.org/spec2.opml) subscription list naming every feed in the library. Import it into your reader to subscribe to all of them in one step, instead of copying each URL by hand. Add `?format=atom` to `/api/opml` for a list of Atom links; the default advertises the RSS ones, which more readers accept.

Each entry carries the feed title, its reader link as `xmlUrl`, and the source page as `htmlUrl`, so a reader can link back to the site the feed was built from. Paused feeds are included, because their links keep serving the stories already saved. A feed that has not yet refreshed successfully is listed, but returns 503 until it does.

The export covers up to 1,000 feeds. A larger library is refused rather than truncated, so the file never silently omits a subscription; the recipe export has the same limit.

An OPML file lists the private read tokens for every feed at once, so it is exactly as sensitive as the reader links themselves. The export requires an active admin session, is never cached, and should be treated like a password file. **Reset feed links** revokes a feed's URLs; export again afterwards to refresh the list.
