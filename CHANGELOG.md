# Changelog

## Unreleased

- Read only what the article skip list needs during a refresh. Deciding which article pages to fetch previously loaded every stored story for the feed, including the article bodies themselves; with a full feed of long articles that was around 9.5 MiB per refresh against 140 KiB now, multiplied by the number of refreshes running at once.
- Log when retention deletes saved stories. `MAX_ITEMS` applies to stories already stored, so lowering it permanently removes the excess at each feed's next refresh; that now appears in the log with the feed and the number removed, and is called out in the configuration reference.
- Show the fetched article body in **Preview items** and make the editor a repair path for it. A selector that matched the wrong part of an article page succeeded silently and could not be corrected: the body was fetched once, preserved by every later refresh, and unaffected by editing the recipe. Previews now display the body, and changing or clearing the article selector discards the stored bodies so later refreshes fetch them again.
- Correct the upgrade documentation for schema 4. The compatibility table described a v0.2.0 database as already current, which told the users most likely to upgrade that no migration would run and no backup was needed. Document the `LOG_LEVEL`, `LOG_FORMAT`, `LOG_MAX_SIZE`, `LOG_MAX_FILES` and `METRICS_TOKEN` settings in the configuration reference, and note that lowering `MAX_ITEMS` permanently deletes already-saved stories.
- Hold a feed when its refresh cannot be stored. A failed database write rolls back the feed's schedule along with everything else, so the source was refetched on every scheduler tick for as long as writing failed, while the log reported the refresh as finished. The feed now backs off, the failure reports its reason, and success is no longer claimed for a refresh that stored nothing.
- Add `LOG_LEVEL` (`debug`, `info`, `warn`, `error`) and `LOG_FORMAT` (`text`, `json`). The startup line now summarizes the running deployment, failed refreshes log their reason and feed title instead of only a success flag, rejected sign-ins are reported with their remote address, and shutdown is logged. Log lines now carry an RFC 3339 timestamp and `level=` field.
- Rotate container logs in Compose so a long-running or noisy deployment cannot fill the host disk. Each service keeps three 10 MiB files by default, configurable through `LOG_MAX_SIZE` and `LOG_MAX_FILES`.
- Add an optional Prometheus metrics endpoint at `/metrics`, enabled by setting `METRICS_TOKEN` and scraped with that token as a bearer credential. It reports library, refresh and capacity aggregates plus per-feed item counts, failure counts and last-success timestamps. Blank leaves the endpoint returning 404.
- Add optional full article content: follow each story's link and publish the article body instead of the list-page teaser. Article pages use the guarded static fetcher by default, with a per-feed option to render them like the list page. Each refresh fetches at most 10 articles so a long feed fills in over several refreshes, a failed article keeps its teaser without failing the refresh, and stored bodies are kept when the list page is re-extracted.
- Advance both databases to schema 4, storing fetched article bodies beside the list-page description. Backups support schemas 1 through 4.
- Add **Export OPML** to download an OPML 2.0 subscription list of every feed, so a reader can subscribe to the whole library in one import. `/api/opml` defaults to the RSS links and accepts `?format=atom`. The file contains every private reader link, so it requires an admin session and is never cached.
- Publish stable container aliases: `latest` and `latest-browser` include Chromium; `latest-static` selects the lightweight runtime. Compose follows these aliases and checks for updates when recreating the service, with version and digest pins still available.
- Preserve direct upgrades from every released SQLite/PostgreSQL schema with frozen schema fixtures and tests that compare all saved feed, item, and run data across upgrade and reopening. Retain every released migration as future schemas are added.

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
