# Recipe export and import

Use **Export recipes** in the library header to download your feed setups. Each feed also has **Export recipe** for an individual file. Use **Import recipes**, select a JSON file, review its feed names, URLs, schedules, and selectors, then choose **Import paused feeds**.

Imports create new copies with fresh RSS/Atom links. They start paused; resume them individually when ready. Importing a file again creates another set of copies. Existing feeds are not matched, merged, or replaced. Validation is repeated when the import is submitted, and the database transaction adds the complete set or none of it. Neither reviewing nor importing a recipe fetches its source.

Exports contain titles, source URLs, refresh intervals (seconds), complete extraction/rendering settings, and optional [story filters](filtering.md). They exclude private RSS/Atom links, internal IDs, authentication settings, saved articles, refresh history, and the one-time option to remove nonmatching saved stories. A recipe export therefore helps move or reuse configurations; use the [database backup procedure](operations.md#backup-and-restore) to preserve items and existing reader URLs.

Files support at most 1,000 recipes and 2 MiB. Each feed configuration must also fit the editor's 64 KiB save limit. Larger libraries can use individual exports. Empty libraries have no export button. Unknown formats, unsupported versions, unknown fields, and invalid recipes are rejected with an explanation. Chromium recipes can be imported into a static deployment as paused configurations; rendering still requires the browser deployment when they are resumed.

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

Filters are optional recipe settings in this same format. Older RSS Workshop versions reject files containing `recipe.filters`; filter-free version-1 files remain compatible. Empty or invalid rule groups are rejected when reviewing and submitting an import. Imported filtered feeds still start paused and do not change existing history.

The [example recipes](examples/) can be selected directly in the import dialog, and use this same versioned format. They build feeds from a demo site that ships beside them, so they work offline and cannot break when a real site is redesigned; [examples/README.md](examples/README.md) explains how to serve it. Preview any recipe before resuming it, particularly one written against a site you do not control.

## API

All routes require an admin session. POST routes also require the normal Origin, JSON, and CSRF checks.

| Route | Behavior |
| --- | --- |
| `GET /api/recipes/export` | Download all portable configurations. |
| `GET /api/recipes/export?id=FEED_ID` | Download one configuration. |
| `POST /api/recipes/preview` | Validate a versioned document and return its feed list/count without saving. |
| `POST /api/recipes/import` | Validate the same document and create paused copies; returns HTTP 201 with `count` and new `ids`. |
