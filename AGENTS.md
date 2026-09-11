# Working on RSS Workshop

This file applies to the entire repository. Use `rss-workshop` for the Go module, executable, Compose service/project, and image names; use **RSS Workshop** in the UI and prose.

## Setup

- Run commands from the repository root. Use the Go version declared in `go.mod`, Python 3.11 or newer (standard library only), and Make. Race tests also require a C compiler.
- Docker Engine and the Compose plugin are needed for container checks. Use Compose 2.24.4 or newer because the overrides use `!reset` and `!override`. Check Docker access before running container tests; `DOCKER='sudo docker'` works with Make's container targets when necessary.
- No Node install or frontend build is needed. Templates, CSS, JavaScript, and database schemas are embedded in the Go executable. Rebuild/restart the server after changing them.
- Check the existing setup before installing tools, recreating services, or changing configuration. `make GO=/absolute/path/to/go ...` supports an existing Go installation outside `PATH`.

```sh
go mod download
make build                 # bin/rss-workshop
make check                 # formatting, race tests, vet, Python checks, native smoke
```

For a disposable native development instance, use a separate port and data directory:

```sh
ADMIN_PASSWORD=local-development-only \
PUBLIC_BASE_URL=http://localhost:8081 \
LISTEN_ADDR=127.0.0.1:8081 \
DATA_DIR="$(mktemp -d)" \
DATABASE_URL= \
make run
```

That password is only for local development. The native process reads exported environment variables; it does **not** load `.env`. Compose loads `.env` automatically. Do not replace an existing `.env` with the example or reuse an operator's database for tests. See [deployment](docs/deployment.md) for the maintained Compose commands and configuration reference.

## Code map

| Location | Responsibility |
| --- | --- |
| `cmd/server` | Startup, dependency wiring, shutdown, version/readiness CLI |
| `internal/config`, `internal/auth` | Environment configuration, admin authentication, sessions and CSRF |
| `internal/model`, `internal/store` | Recipes/items, SQLite/PostgreSQL schemas, persistence and imports |
| `internal/fetch` | Static HTTP fetching and destination validation |
| `internal/browser` | Bounded Chromium pool and network proxy |
| `internal/flaresolverr` | Optional external FlareSolverr client |
| `internal/extract` | CSS/XPath extraction, content sanitization, URLs and dates |
| `internal/filter` | Bounded literal keyword trees and visible-description matching |
| `internal/diagnostics` | Bounded, redacted refresh and preview traces |
| `internal/scheduler` | Shared job limits, refresh scheduling, conditional requests and retries |
| `internal/feed` | RSS and Atom serialization |
| `internal/web` | Management/reader routes, visual selection, embedded templates and assets |
| `testdata`, `docs/examples` | Deterministic HTML fixtures, plus a demo site and the importable recipes built from it |
| `scripts` | Smoke, backup/restore and release-source tools with Python tests |
| `.github/workflows`, `deploy` | CI, manual image publication and Chromium sandbox profile |

## Behavior to preserve

- Serialization never reads the clock: every value published comes from stored state, so unchanged stories produce identical bytes and a polling reader keeps receiving 304. RSS keeps the full article in `description` and repeats it in `content:encoded`; Atom splits the list-page description into `summary` and the article into `content`. The extracted image is published as media and inline, but only once in the body. Changing any of this rewrites every feed and forces every subscriber to refetch, so batch such changes rather than shipping them one at a time.
- Reader requests serialize saved items; they must never fetch or render the source. Failed or empty extraction preserves previously saved output. Publication dates and GUIDs remain stable across refreshes, including relative-date estimates.
- `MAX_ITEMS` is retention over everything stored, not a cap on new stories, so lowering it deletes saved history at the next refresh. It is the only irreversible effect available through configuration: report it in the log when it removes rows, and keep it documented as destructive.
- A refresh whose result cannot be stored has changed nothing, including the feed's schedule, so the scheduler holds that feed in memory with its own backoff. Without it the source is refetched every tick for as long as the database is unwritable. Such a refresh must report the failure and must not report success.
- Every Compose service rotates its container log. Docker retains logs until the disk fills, so an unbounded service log is a denial-of-service vector for anything that can make the app write lines. Keep the cap when adding a service.
- `/metrics` is disabled unless `METRICS_TOKEN` is set, and then requires that token as a bearer credential; an admin session is deliberately not sufficient, because a scraper cannot hold one. Series may name feeds but must never carry reader links, source URLs, or story content, and per-feed series stay bounded. Logs follow the same rule: feed titles are allowed, source URLs, reader tokens, passwords and cookies are not.
- Full article content is optional and per feed. Article pages go through the same guarded fetcher and destination validation as list pages; rendering them is an explicit opt-in because the browser pool is shared. Keep the per-refresh article cap, the fetch-once-then-recency rule and the bounded preview sample. A failed article keeps the list-page description and must never drop the story or fail the refresh. Fetched bodies are stored separately from that description so re-extracting the list page each refresh still updates late-published images without discarding retrieved articles. Article pages must never change an item's identity, date or link.
- Changing or clearing a recipe's article selector discards that feed's stored article bodies in the same transaction as the edit, because a body is otherwise fetched once and preserved by every later merge. The editor is the repair path for a selector that captured the wrong block; an unrelated edit must keep correct bodies. Previews display the fetched body so the mistake is visible before saving.
- Schema 4 stores fetched article bodies. Its merge replaces a stored body only when a refresh actually fetched one; an empty value means "not fetched this time", never "the article is empty".
- The OPML export lists every feed's private reader link, so it stays behind authentication, uncached, and out of recipe exports. Its `xmlUrl` values must remain the links the reader routes actually serve.
- Reading per-feed diagnostics must not fetch or queue a source. Keep the latest 50 refresh runs per feed; previews do not write history. Preserve legacy summaries, bounded/redacted traces, and plain-text expected/received field samples. Missing optional fields must not reject otherwise valid items.
- Filtering happens after required-field validation. Zero included items with valid extracted stories is successful and must not trigger Auto fallback or error backoff. Preserve existing history by default; only the explicit save option may prune nonmatching stored stories, atomically with the recipe edit. Never import that one-time option. Keep the keyword/tree/recipe-size bounds, bulk entry, and identical preview/refresh matching.
- Static mode must work in the minimal image without Chromium or FlareSolverr. Optional modes share extraction, sanitization and persistence. Keep FlareSolverr an explicit opt-in; its remote network boundary differs from the local guarded fetcher.
- Plain `compose.yaml` downloads the published Chromium image with its sandbox and resource limits. `compose.static.yaml` selects the smaller published runtime and clears browser-only settings. With `RSS_IMAGE` blank, the browser tracks `latest` (also `latest-browser`) and static tracks `latest-static`. Operator defaults use `pull_policy: always`; build overrides use `build`, and private smoke fixtures use `never`. An explicit tag or digest must match the runtime. Container checks must exercise aliases, pins, pull policies and local builds.
- Operator Compose files contain no local build. Development explicitly adds `compose.build.yaml` for Chromium, or `compose.build.static.yaml` after `compose.static.yaml` for the static runtime. Preserve their local image names and build policy; see [deployment](docs/deployment.md#build-container-images-from-source). Native release archives support Linux amd64/arm64, include license notices, and use the host's CA store and optional Chromium; they do not load `.env` automatically.
- Keep concurrency, response sizes, item counts, timeouts and cancellation bounded. Recipe edits must invalidate stale in-flight results and conditional-fetch validators.
- Preserve destination validation through redirects and exact-IP dialing. Do not bypass it with environment proxies, broad private-network exceptions, or a disabled Chromium sandbox. Local test fixtures use explicit, narrowly scoped network exceptions.
- The shipped example recipes target the demo site in `docs/examples/demo-site`, not third-party publishers: they must keep working offline, and a test imports every example and extracts from that site. Do not add examples that scrape a real site.
- Every response carries the security headers set in `Handler`, including unauthenticated and error responses. The content policy is the second layer behind server-side sanitization for extracted markup, so both layers stay tested; the visual selector frame is the one deliberate override and replaces the policy with a stricter sandboxed one.
- Management mutations require authentication, Origin validation and CSRF protection. Keep the visual page preview isolated and sanitized. Preserve dark mode, field help, and live CSS/XPath highlighting when changing the editor.
- Require configured admin credentials, without imposing a length policy on `ADMIN_PASSWORD`. Preserve full-password matching for long/Unicode values and compatibility with externally supplied bcrypt hashes.
- `PUBLIC_BASE_URL` is the stable public origin without a path prefix. Temporary forwarded browser ports do not become deployment configuration. Check the existing origin tests before changing this behavior.
- Keep SQLite as the default when `DATABASE_URL` is blank or unset. An explicit `postgres://` or `postgresql://` URL selects PostgreSQL; never silently migrate, merge, or replace data when switching. PostgreSQL connections require UTF-8 database/client encoding. Database credentials are trusted configuration, separate from source-fetching `ALLOW_CIDRS`.
- Docker persistence uses precreated host bind directories: `RSS_DATA_DIR` at `/data` and optional `POSTGRES_DATA_DIR` at `/var/lib/postgresql`. Preserve `create_host_path: false`, the app's UID/GID 65532 permissions, and the fixed container `DATA_DIR=/data`. Existing named-volume installations require an explicit migration before changing mounts.
- Run one application process per database for both backends. Schema changes need explicit transactional migrations and a backup/restore compatibility review; do not silently recreate existing data. Preserve storage behavior across both backends, including imports, recipe-version checks, scheduling, GUIDs, dates, retention, and reader output.
- Schema 2 adds run diagnostics; schema 3 prevents older apps from silently ignoring filters stored in recipe JSON. Retain every released migration and frozen schema fixture so users can upgrade directly from any released schema without installing intermediate app versions. Test the full transactional chain, existing history preservation and rollback on failure. Schema 4 stores fetched article bodies. Keep backup support for every released schema, and derive the tool's user-facing version list from its supported set rather than repeating it; unknown/newer schemas must still be rejected. Downgrades require a backup compatible with the target app version.

## Validation

Use focused tests while editing, then run `make check` for Go or application changes. It includes the deterministic native workflow with temporary credentials, local fixture servers and a disposable database. `make test` and `make check` skip the opt-in Chromium and live-site checks. Their Go commands target `./cmd/... ./internal/...` to avoid walking private runtime and backup directories; use the same package paths for direct Go test commands.

| Change | Additional checks |
| --- | --- |
| Visual editor, embedded UI or Chromium integration | `make browser-test` (sandboxed Chromium and UI fixture tests) |
| Dockerfiles, Compose, executable paths or deployment wiring | `make compose-test` (merged configuration) and `make docker-smoke` (both images, including restart persistence) |
| Both of the above | `make check-containers` |
| Backup/restore tooling | `make backup-test` and the opt-in Docker roundtrip below |
| Storage, database configuration or scheduler persistence | `make postgres-test` with a disposable `RSS_TEST_POSTGRES_URL`, plus `make docker-postgres-smoke` |
| Source inventory/release tooling | `make release-test`; lint workflows with `actionlint` if available |
| Docs only | Check local links, commands, names and consistency with current code/configuration |

```sh
# Builds a local static image, then exercises backup/restore using isolated storage.
make docker-static
RSS_BACKUP_DOCKER_TEST=1 RSS_BACKUP_TEST_IMAGE=rss-workshop:static-check \
  python3 scripts/backup_test.py
```

The standalone backup tests require direct Docker CLI access, independently of Make's `DOCKER` variable. If elevation is required, use an explicit command such as `sudo env RSS_BACKUP_DOCKER_TEST=1 RSS_BACKUP_TEST_IMAGE=rss-workshop:static-check python3 scripts/backup_test.py`. The smoke script accepts `DOCKER`, which Make's container targets pass through. Avoid passing the entire environment through sudo.

PostgreSQL tests require a dedicated disposable database via `RSS_TEST_POSTGRES_URL`; never point them at a real library. `make postgres-test` runs PostgreSQL persistence checks, `make postgres-smoke` runs the native workflow against that test database, and `make docker-postgres-smoke` creates its own temporary Compose server and storage. Normal SQLite checks explicitly clear `DATABASE_URL`. Compose tests must supply private fixture paths for `RSS_DATA_DIR` and `POSTGRES_DATA_DIR` rather than inheriting operator settings. See [PostgreSQL setup](docs/postgresql.md) for the service contract and PostgreSQL backup commands; the SQLite backup utility must not be used for PostgreSQL.

Browser checks store synthetic screenshots under ignored `artifacts/browser/`. Tests need local listening sockets; a restrictive execution sandbox may require running them with the appropriate permission. Report a skipped or blocked check accurately. Prefer deterministic fixtures to third-party websites; live-site tests are optional and their contents can change. Large feed-count load tests are not part of the required checks.

## Repository and operational hygiene

- Keep `.env`, passwords, cookies, reader bearer tokens, databases, backups and private source pages out of commits, logs and screenshots. Avoid printing resolved Compose configuration or full container environment inspection because they contain secrets.
- Preserve existing services and data. Identify the current project, container and storage mount before changing deployment settings. Use [operations](docs/operations.md) for verified backups, migration from named volumes, and restore into new storage; never replace a mount with an empty path or use `docker compose down -v` as routine cleanup on a real deployment. Keep custom host storage outside the checkout or excluded from Git and Docker builds. Test scripts clean up their own isolated resources.
- For Gluetun deployments, follow [VPN networking](docs/gluetun.md): publish ports on Gluetun and keep app network sharing intact. A namespace fixture tests Docker wiring, not a real VPN connection or its kill switch; report those verification limits. Keep site-specific overrides in ignored `compose.local.yaml`.
- Keep lasting documentation in the README, contributor guide and relevant topic guide. Update the existing guide when behavior changes; do not add implementation plans, dated progress logs, local-machine readiness reports or temporary test paths to the repository.
- Preserve `LICENSE` and `docs/licenses/`. Dependency or image changes may require updating notices, inventories and matching source archives; follow [licensing](docs/licensing.md) and [releases](docs/releases.md).
- Keep changes focused. Add regression tests for meaningful behavior or failure boundaries, rather than tests that repeat implementation details. Include what changed and what validation actually ran in the handoff.
- GitHub and registry publication use the separate manual release process. Stable aliases may advance only after both immutable version manifests and their source checks succeed; preserve prerelease exclusion, downgrade protection and repairable partial promotions. Do not turn local development or testing into a release implicitly.
