# RSS and Atom readers

Each saved feed provides both **Open RSS feed** and **Open Atom feed**, with copy buttons beside them. Both formats read the same persisted items and never trigger scraping. Their URLs end in `.xml` and `.atom` respectively. Choose whichever your reader supports.

The formats share one private read token. **Reset feed links** revokes both old URLs. The replacement Atom self link changes, while its feed ID and item IDs remain stable. A new recipe import creates a separate feed identity and separate reader URLs.

Atom responses use `application/atom+xml; charset=utf-8`, the Atom 1.0 namespace, absolute alternate/self links, escaped HTML content, and content-derived ETags. Conditional requests support `If-None-Match`, including weak tags. RSS and Atom have separate ETags. New feeds return HTTP 503 with `Retry-After: 60` until their first successful refresh; failed or empty extraction retains the last successful content.

Atom entry IDs use the same stored GUIDs as RSS. Publication dates remain the saved publication/first-seen values. Entry `updated` uses the last successful observation because the current database does not store a separate per-item content-change timestamp; feed `updated` uses the latest successful refresh or entry timestamp. These are stored timestamps, so reader requests alone never change output bytes. The feed title is used as the fallback feed author, since recipes do not yet extract authors.

Reader responses do not support Last-Modified handling. Recipe files are covered by the [export/import guide](recipe-portability.md). The Atom representation follows [RFC 4287](https://www.rfc-editor.org/rfc/rfc4287).
