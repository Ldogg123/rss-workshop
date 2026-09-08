# Changelog

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
