# Recipe export and import

Use **Export recipes** in the library header to download your feed setups. Each feed also has **Export recipe** for an individual file. Use **Import recipes**, select a JSON file, review its feed names, URLs, schedules, request headers, Quiet thresholds, selectors, and cleanup rules, then choose **Import paused feeds**.

Imports create new copies with fresh RSS/Atom links. They start paused; resume them individually when ready. Importing a file again creates another set of copies. Existing feeds are not matched, merged, or replaced. Validation is repeated when the import is submitted, and the database transaction adds the complete set or none of it. Neither reviewing nor importing a recipe fetches its source.

Exports contain titles, source URLs, refresh intervals (seconds), complete extraction/rendering settings, a feed's own [User-Agent](browser.md#recipe-settings) and [Accept-Language](browser.md#recipe-settings) when it has them, its [Mark as Quiet after](operations.md#quiet-feeds) setting unless it is Automatic, its [content cleanup](atom.md#content-cleanup) rules, and optional [story filters](filtering.md). They exclude private RSS/Atom links, internal IDs, authentication settings, saved articles, refresh history, and the one-time option to remove nonmatching saved stories. A recipe export therefore helps move or reuse configurations; use the [database backup procedure](operations.md#backup-and-restore) to preserve items and existing reader URLs.

Files support at most 1,000 recipes and 2 MiB. Each feed configuration must also fit the editor's 64 KiB save limit. Larger libraries can use individual exports. Empty libraries have no export button. Unknown formats, unsupported versions, unknown fields, and invalid recipes are rejected with an explanation. An XPath recipe whose selector holds leftover CSS, such as `.summary`, is not one of them: earlier releases saved and exported such recipes, so the file imports, and the review shows a [warning](visual-selector.md#limits) under that recipe naming the selector to rewrite. Chromium recipes can be imported into a static deployment as paused configurations; rendering still requires the browser deployment when they are resumed.

## Version 1 format

```json
{
  "format": "rss-workshop.recipes",
  "version": 1,
  "feeds": [
    {
      "title": "Example news",
      "url": "https://example.com/news",
      "interval": 900,
      "recipe": {
        "mode": "static",
        "type": "css",
        "items": "article",
        "title": {"selector": "h2", "attr": ""},
        "link": {"selector": "a", "attr": "href"}
      }
    }
  ]
}
```

Optional recipe fields use the same defaults as the editor. Future incompatible changes require a new version; this server explicitly accepts version 1.

A feed's own User-Agent is the optional `recipe.user_agent`, left out when blank, so a file from a library that sets none is unchanged. Imports trim surrounding spaces and reject a value longer than 256 characters or with anything but printable ASCII. Releases before 1.2 reject a file in which any recipe has one, as an unknown field, and import none of its feeds.

A feed's Accept-Language is the optional `recipe.accept_language`, also left out when blank, so a file that sets none is unchanged and older releases still import it. Imports trim surrounding spaces and reject anything but at most 10 language ranges in at most 128 characters, such as `de-CH, de;q=0.9, en;q=0.5`. Releases before 1.3 reject a file in which any recipe has one, as an unknown field, and import none of its feeds; remove the field from such a recipe to import it there.

A feed's **Mark as Quiet after** setting is the optional `recipe.quiet_days`: `-1` for Never, or `7`, `30`, `90` or `365` days. Automatic is left out, so a file that sets none is unchanged and older releases still import it. Imports reject any other value. Releases before 1.3 reject a file in which any recipe has one, as an unknown field, and import none of its feeds; remove the field from such a recipe to import it there.

Content cleanup rules are the optional `recipe.remove`, a list of selectors in the recipe's selector type (CSS for a feed source), left out when empty, so a file without rules is unchanged and older releases still import it. Imports trim the space around each rule, drop blank ones, and reject more than 20 rules, a rule longer than 1,000 characters or containing a line break, an invalid selector, or an XPath rule that does not start with `./`. Releases before 1.3 reject a file in which any recipe has rules, as an unknown field, and import none of its feeds. See [content cleanup](atom.md#content-cleanup).

A [feed source](feed-sources.md) is a recipe with `"type": "feed"`, the fetch mode `static` and no item or field selectors; an article selector, if it has one, is CSS. The import review names it an RSS or Atom feed instead of listing selectors. Releases before 1.3 reject a file in which any recipe is a feed source, with `selector type must be css or xpath`, and import none of its feeds.

Filters are optional recipe settings in this same format. Older RSS Workshop versions reject files containing `recipe.filters`; filter-free version-1 files remain compatible. Empty or invalid rule groups are rejected when reviewing and submitting an import. Imported filtered feeds still start paused and do not change existing history.

[Library filters](filtering.md#library-filters) belong to one server, so exports copy them into each recipe: `recipe.filters` holds the feed's own rules combined with every library filter it uses. An imported feed keeps those rules as its own and is not linked to a library filter, even on the server that exported it. To copy a feed within one server, use **Duplicate** on its card instead: it needs no file, and the copy stays linked to the same library filters. See [duplicating a feed](operations.md#the-library-dashboard).

The [example recipes](examples/) can be selected directly in the import dialog, and use this same versioned format. They build feeds from a demo site that ships beside them, so they work offline and cannot break when a real site is redesigned; [examples/README.md](examples/README.md) explains how to serve it. Preview any recipe before resuming it, particularly one written against a site you do not control.

## API

All routes require an admin session. POST routes also require the normal Origin, JSON, and CSRF checks.

| Route | Behavior |
| --- | --- |
| `GET /api/recipes/export` | Download all portable configurations. |
| `GET /api/recipes/export?id=FEED_ID` | Download one configuration. |
| `POST /api/recipes/preview` | Validate a versioned document and return its feed list/count without saving. |
| `POST /api/recipes/import` | Validate the same document and create paused copies; returns HTTP 201 with `count` and new `ids`. |
