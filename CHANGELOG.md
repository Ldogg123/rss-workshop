# Changelog

## v1.0.0 — 2026-09-12

First stable release. RSS Workshop turns pages that publish no feed into RSS and Atom feeds you keep, and 1.0 is the point at which its upgrade path, configuration surface and reader URLs become commitments rather than implementation details. See [upgrades](docs/operations.md#upgrades) before installing over an existing deployment.

### Upgrading from v0.2.0

- **Take a backup first.** Starting this version migrates the database to schema 4 in a single transaction. Every released schema upgrades directly, but an older release cannot open a newer database, so rolling back needs a backup taken *before* the upgrade. See [backup and restore](docs/operations.md#backup-and-restore).
- **Readers refetch once.** Published feeds carry more than they did, so every feed's bytes change once and subscribed readers fetch a full copy on their next poll. Stories keep their identities and publication dates, so nobody sees a duplicate.
- **Container logs now rotate**, at three 10 MiB files per service. `docker compose logs` reaches back only as far as the retained files.

### Feeds readers actually want

- **Full article content.** Follow each story's own link, extract the article body, and publish that instead of the list page's one-line teaser. Off by default and configured per feed. Article pages are fetched with the same guarded fetcher as list pages, at most 10 per refresh so a long feed fills in over several cycles, and an article that cannot be fetched keeps its teaser rather than dropping the story or failing the refresh.
- **Better published feeds.** RSS keeps the article in `description` and repeats it in `content:encoded`; Atom carries the list-page description as `summary` and the article as `content`. The extracted image is published as `media:content` and as an Atom enclosure, where readers look for a thumbnail, and is no longer repeated when the article body already contains it. The RSS channel gains `atom:link rel="self"`, `ttl`, `lastBuildDate` and `generator`.
- **Conditional requests.** Reader responses honour `If-Modified-Since` alongside the existing ETag, so a reader polling from several devices costs validators rather than full copies.
- **OPML export.** Download a subscription list of every feed and import it into your reader in one step. It contains every private reader link, so it requires an admin session and is never cached.

### Knowing when something breaks

- **Log levels.** `LOG_LEVEL` selects `debug`, `info`, `warn` or `error`, and `LOG_FORMAT` switches to JSON for a log collector. The startup line summarizes the running deployment, failed refreshes report their reason and feed, rejected sign-ins are reported with their remote address, and shutdown is logged.
- **Prometheus metrics.** Setting `METRICS_TOKEN` enables `/metrics`, scraped with that token as a bearer credential; blank leaves the endpoint returning 404. Per-feed series carry item counts, consecutive failures and last-success timestamps, so an alert can name the feed that broke.
- **A failed database write no longer hammers the source.** Such a refresh rolls back the feed's schedule along with everything else, so the source was refetched on every scheduler tick for as long as writing failed, while the log reported success. The feed now backs off, and the failure reports its reason.
- **Retention is visible.** `MAX_ITEMS` applies to stories already saved, so lowering it permanently deletes the excess at each feed's next refresh. That now appears in the log and is documented as destructive.

### Running it

- **Reverse proxy support.** `compose.proxy.yaml` and a [guide](docs/reverse-proxy.md) for a proxy running in Docker, which cannot reach the default loopback publication.
- **Container log rotation**, configurable through `LOG_MAX_SIZE` and `LOG_MAX_FILES`.
- **Stable image aliases.** `latest` and `latest-browser` include Chromium; `latest-static` selects the lightweight runtime. Compose follows these aliases, with version and digest pins still available.
- **Direct upgrades from every released schema**, with frozen per-backend fixtures and tests comparing all saved feed, item and run data across upgrade and reopening. Backups support schemas 1 through 4.

### Editor and examples

- **Full article content is inspectable and repairable.** Previews show the fetched body, so a selector matching the wrong block is visible before saving, and changing or clearing the selector discards the stored bodies so later refreshes fetch them again.
- **New example recipes.** The previous examples targeted five real news sites that all publish their own feeds, extracted only a title and link, and two relied on generated class names that change whenever those sites deploy. They are replaced by a demo site included in the repository, with recipes covering every field, CSS and XPath, and filtering with full article content. A test imports each example and extracts from that site.
- **Reorganized documentation.** The README follows install, first feed, and diagnosing a broken one, with installation variants collected below; `.env.example` is grouped by purpose. Screenshots are refreshed and now include full article content and per-feed diagnostics.

### Internal

- Security headers applied to every response are pinned by a test; the content policy is the second layer behind sanitization for extracted markup and previously had no coverage of its own.
- A refresh no longer loads every stored article body to decide which article pages to fetch.

## v0.2.0 — 2026-09-08

- Add optional title, description, and link filters with nested All/Any include/exclude rules, case-insensitive literal phrases, and bulk lists of up to 500 keywords. Previews show included/filtered counts and excluded examples; intentionally empty filtered results succeed without browser fallback.
- Make applying filters to saved history an explicit editor option. Preserve existing stories by default and keep filtering settings in recipe exports/imports.
- Advance both databases to schema 3 so older app versions cannot silently ignore stored filters. Preserve diagnostics and support backups of schemas 1, 2, and 3.
- Add screenshots of the dashboard, visual editor, and filtering controls to the README.
- Run CI once per pull-request update, skip documentation-only changes, and cancel superseded automatic runs while keeping release validation separate.

## v0.1.2 — 2026-09-08

- Make Compose use published images by default, including the browser and static runtimes. Move local builds into explicit development overrides and simplify installation commands.
- Show match counts and per-item rejection reasons when a feed preview fails, so missing titles and links can be diagnosed in the editor.
- Add per-feed diagnostic history and detailed preview traces with fetch mode, HTTP status, timing, match counts, and rejection reasons. Include expected field values and short samples of unexpected text, including invalid dates and temporarily missing images.
- Upgrade SQLite and PostgreSQL databases transactionally from schema 1 to 2 to retain diagnostics with the last 50 runs per feed. Keep legacy run summaries and support backups of both schemas; downgrading requires a pre-upgrade backup.

## v0.1.1 — 2026-09-08

- Remove the admin password length policy. Short passwords and long or Unicode passphrases work through `ADMIN_PASSWORD`; existing externally generated bcrypt hashes remain supported.
- Default the documented Docker installation to published images, and provide native Linux executable downloads with checksums and setup instructions.

## v0.1.0 — 2026-09-07

- Self-hosted RSS and Atom feeds with a single-admin web interface, scheduled refreshes, SQLite persistence, stable item identities/dates, and revocable reader links.
- Optional PostgreSQL storage through `DATABASE_URL`, existing-server and dedicated Compose setups, and PostgreSQL backup/restore guidance; SQLite remains the default.
- Visual card and field selection with live CSS/XPath tuning, field tips, and light/dark themes.
- Automatic relative publication dates, exact timestamp preference, and estimated-date previews.
- Static HTTP, sandboxed Chromium/Auto, and optional external FlareSolverr fetch modes.
- Recipe export/import as paused copies and verified database backup/restore tooling.
- Lightweight static and browser images, version metadata, CI, and a manual GHCR release workflow.
- Prebuilt native Linux AMD64 and ARM64 executable archives, with license notices, SHA-256 checksums, and a separate manual release workflow.
- Optional Compose networking through an existing Gluetun container, with port-conflict, database, and remote-solver guidance.
- Configurable host directories for SQLite and PostgreSQL persistence, with verified SQLite directory restore and migration from existing Docker volumes.
- Browser-enabled default Compose installation, with an explicit lightweight static override and local-folder quickstart examples for SQLite and PostgreSQL.

See [release preparation](docs/releases.md) for validation, matching source archives, and publication steps.
