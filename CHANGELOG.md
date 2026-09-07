# Changelog

## Unreleased

- Self-hosted RSS and Atom feeds with a single-admin web interface, scheduled refreshes, SQLite persistence, stable item identities/dates, and revocable reader links.
- Optional PostgreSQL storage through `DATABASE_URL`, existing-server and dedicated Compose setups, and PostgreSQL backup/restore guidance; SQLite remains the default.
- Visual card and field selection with live CSS/XPath tuning, field tips, and light/dark themes.
- Automatic relative publication dates, exact timestamp preference, and estimated-date previews.
- Static HTTP, optional sandboxed Chromium/Auto, and optional external FlareSolverr fetch modes.
- Recipe export/import as paused copies and verified database backup/restore tooling.
- Lightweight static and browser images, version metadata, CI, and a manual GHCR release workflow.
- Optional Compose networking through an existing Gluetun container, with port-conflict, database, and remote-solver guidance.
- Configurable host directories for SQLite and PostgreSQL persistence, with verified SQLite directory restore and migration from existing Docker volumes.
- Browser-enabled default Compose installation, with an explicit lightweight static override and local-folder quickstart examples for SQLite and PostgreSQL.

See [release preparation](docs/releases.md) for validation, matching source archives, and publication steps.
