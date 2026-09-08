# Changelog

## Unreleased

- Make Compose use published images by default, including the browser and static runtimes. Move local builds into explicit development overrides and simplify installation commands.
- Show match counts and per-item rejection reasons when a feed preview fails, so missing titles and links can be diagnosed in the editor.

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
