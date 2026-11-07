# Operations

## Health and logs

`GET /healthz` checks liveness; `GET /readyz` checks the selected database connection. The container uses `/rss-workshop -healthcheck` for readiness. Use the same Compose files and project options as your deployment:

```sh
docker compose ps
docker compose exec -T rss-workshop /rss-workshop -healthcheck
docker compose logs --tail 30 rss-workshop
```

The dashboard reports saved items, refresh status, due feeds, and fetch capacity. Chromium/FlareSolverr capacity indicates configuration, not remote-site health. A FlareSolverr failure on a card, in a preview or in Diagnostics names the step that failed, such as a DNS lookup, a refused connection or an address that is not the FlareSolverr API; [FlareSolverr troubleshooting](flaresolverr.md#troubleshooting) says what to check for each. A card marked **Waiting to retry** is [held after a failed save](#waiting-to-retry-after-a-failed-save), and one marked **Quiet** has [stopped changing](#quiet-feeds).

### The library dashboard

Each feed card shows one status: **Paused**, **Waiting to retry**, **Needs attention** (the latest refresh failed), **Pending** (no successful refresh yet), **Quiet**, or **Active**, checked in that order. **What each status means** under the toolbar explains them. The card also lists its saved items, next refresh and how many refreshes in a row have failed, and when it last attempted a refresh, last succeeded and last changed its output.

The toolbar above the cards narrows and orders the library without contacting the server:

- **Search feeds** matches feed names and the site part of each source address, ignoring case and repeated spaces. It does not match the rest of the address.
- **Status** shows only the cards with that badge.
- **Sort by** orders the cards by name, next refresh (soonest first, paused feeds and feeds waiting to retry last), last success or last change (oldest first, with never first), or failures in a row (most first). Ties keep name order.

The dashboard reloads the library every 10 seconds and keeps the search, the selections and the keyboard focus while it does. The browser remembers the status and sort for the next visit, in its own storage; the search starts empty each time. A browser that blocks site storage gets the defaults instead. When nothing matches, **Show all feeds** clears the search and the status.

**Duplicate** on a card opens the editor with a copy of the feed: its address, schedule, fetch mode and render settings, its own User-Agent, selectors, date settings, full article content, its own story filters and the same [library filters](filtering.md#library-filters), linked rather than copied. The name becomes `Copy of` and the original name, shortened to fit the 200-byte limit. Nothing is saved until you choose **Save feed**, which adds a new feed with its own reader links and no saved stories, through the same validation and size limits as any new feed; the original is not changed. The copy starts paused, like an import, unless you check **Refresh automatically**, and it never offers to remove saved stories. A reader subscribed to both feeds sees the stories they share twice.

### Quiet feeds

A refresh that finds nothing new succeeds, so a feed can stop delivering stories without ever failing: the site may have stopped publishing, moved its news to another page, or started serving a page whose matched cards never change. The dashboard marks such a feed **Quiet** when it is enabled, its latest refresh succeeded, and its published output has not changed for **14 days, or 10 refresh intervals if that is longer**. A feed refreshed every 15 minutes, every hour or every day is quiet after 14 days; one refreshed every 2 days after 20, and one refreshed weekly after 70. A source that answers `304 Not Modified` indefinitely counts as unchanged.

The card says how long its output has been unchanged. Open the source page to see whether it still lists new stories, then **Edit** and **Preview items** to check the selectors. Some sites publish rarely: there is no per-feed switch to silence the badge, but a longer refresh interval raises the threshold, and also fetches the site less often.

Quiet reads the same modification time readers receive as `Last-Modified`, which moves when anything the feed publishes changes, not only when a story is added: an edited story, a new image, a story removed by retention, or a change to the feed's title, address or refresh interval. A feed whose stories change on every refresh, such as one with a relative age or a comment count in its descriptions, is therefore never quiet. Pausing and resuming keep the time, and so does a change to the list-page selectors until a refresh with them publishes something different. It is computed from the stored feed alone, so the dashboard's 10-second poll adds no database work for it. A feed [waiting to retry](#waiting-to-retry-after-a-failed-save) is not quiet, because its latest refresh was never stored. `GET /api/feeds` reports each feed's `last_changed` time and `quiet` flag.

### Log detail

`LOG_LEVEL` selects how much reaches `docker compose logs`:

| Level | Reports |
| --- | --- |
| `debug` | Everything below, plus scheduler activity when feeds are due |
| `info` (default) | Startup configuration, sign-ins, shutdown, and each completed or discarded refresh |
| `warn` | Only problems: failed refreshes with their reason, and rejected sign-ins |
| `error` | Only failures: the server stopping, the database errors that abort a scheduler tick or discard a completed refresh, and selector library failures |

The startup line summarizes the running deployment — version, commit, database backend, worker count, whether Chromium, FlareSolverr and metrics are enabled — which is the quickest way to confirm a container is running the configuration you intended.

A failed refresh logs the feed title, its consecutive failure count, and the reason; a successful one logs the item count, HTTP status, and duration. Feed titles appear because they are your own labels, but source URLs, reader links, passwords and session cookies are never logged. A run of `login rejected` lines with a remote address is worth investigating.

`refresh discarded` means the feed was edited or deleted while it refreshed. Its result was thrown away unsaved, so Diagnostics shows no run for it, and an edited feed refreshes again with its new settings. `refresh not saved` is an error: the database refused a finished refresh, for example because its disk is full, and the feed now [waits to retry](#waiting-to-retry-after-a-failed-save).

`selector library panicked` means the CSS or XPath library failed on a page; see [the visual selector limits](visual-selector.md#limits). The refresh fails like any other, and the line names the field, the type of failure and a short stack for a bug report. It leaves out the page value that caused it and the selector, because either can hold a URL.

`LOG_FORMAT=json` emits one JSON object per line for a log collector such as Loki or Elasticsearch. The default `text` stays easier to read directly.

Docker keeps container logs until the disk fills, so the Compose files rotate them: each service keeps `LOG_MAX_FILES` files of at most `LOG_MAX_SIZE`, defaulting to three 10 MiB files per service. Raise them in `.env` if you need longer history, and remember that `docker compose logs` only reaches back as far as the retained files. To use a different logging driver, such as `journald` or a remote collector, override the `logging` block in an ignored `compose.local.yaml`; its options are driver-specific, so `max-size` and `max-file` may not apply.

### Metrics

Setting `METRICS_TOKEN` enables `GET /metrics` in the Prometheus text format. Leave it blank and the endpoint returns 404, which is the default. Generate a token with `python3 -c 'import secrets; print(secrets.token_hex(24))'` and scrape it as a bearer credential:

```yaml
scrape_configs:
  - job_name: rss-workshop
    authorization:
      credentials: YOUR_GENERATED_TOKEN
    static_configs:
      - targets: ["rss-workshop:8080"]
```

Aggregates cover the library, saved stories, and refresh/Chromium/FlareSolverr capacity. Per-feed series are labelled with the feed title and its internal ID, so an alert can name the feed that broke:

| Metric | Use |
| --- | --- |
| `rss_workshop_feeds_failing` | Feeds whose last refresh failed |
| `rss_workshop_feeds_held` | Enabled feeds [waiting to retry](#waiting-to-retry-after-a-failed-save) a refresh the database could not store. They still count in `rss_workshop_feeds_due`, because their stored schedule says they are due |
| `rss_workshop_feeds_quiet` | Feeds the dashboard marks [quiet](#quiet-feeds): still refreshing, but with output unchanged past the threshold. It counts the whole library |
| `rss_workshop_feed_failures` | Consecutive failures for one feed |
| `rss_workshop_feed_last_success_timestamp_seconds` | Alert when a feed has not succeeded for too long; `0` means it never has |
| `rss_workshop_feed_last_changed_timestamp_seconds` | When a feed's published output last changed, the time readers receive as `Last-Modified`; `0` means it has published nothing |
| `rss_workshop_feed_items` | Stories saved for one feed |
| `rss_workshop_refresh_active` / `_capacity` | Whether refreshes are queueing |

An alert for one feed that stops changing, with your own threshold rather than the dashboard's:

```yaml
- alert: FeedOutputUnchanged
  expr: |
    (time() - rss_workshop_feed_last_changed_timestamp_seconds > 7 * 86400)
    and on (id) rss_workshop_feed_enabled == 1
    and on (id) rss_workshop_feed_failures == 0
    and on (id) rss_workshop_feed_last_changed_timestamp_seconds > 0
  for: 1h
  annotations:
    summary: "{{ $labels.feed }} has published nothing new for a week"
```

Because the series name your feeds, the token is a real credential: keep it out of shared dashboards and rotate it by changing `METRICS_TOKEN` and recreating the service. Metrics never include reader links, source URLs, or story content. A library beyond 1,000 feeds keeps every aggregate, but per-feed series cover only the first 1,000 feeds by title, so a scrape stays bounded. Feeds after that have no per-feed series; `rss_workshop_feeds` still counts them.

These examples use the default published browser image, or the matching image selected by `RSS_IMAGE` in `.env`. Include any static, PostgreSQL, VPN, storage, or development build overrides used by your deployment.

### Feed diagnostics

Choose **Diagnostics** on a saved feed to see its latest refresh results, newest first. Expand a run for the requested fetch mode, recipe version, source HTTP status, duration, response size (for a rendered page, the rendered HTML, and for a static page decoded from another encoding, its size in UTF-8), matched elements, valid stories, included/filtered counts, and field warnings. Auto records both static and Chromium attempts when it falls back, so the initial matching failure remains visible. A failed request without an HTTP response shows no recorded status; a `304` means the source reported no changes and saved items were kept.

**Reload history** only rereads the database. To fetch the source again, use the feed's **Refresh** action. The authenticated history API is `GET /api/feeds/{id}/runs`. Older run records still show their summary; detailed traces are available for refreshes performed after upgrading.

In the editor, **Preview items** includes warnings such as an expected date receiving unrelated text, a selector matching nothing, or a missing `href` attribute. **Preview diagnostics** adds fetch and matching details to both successful and failed previews. Missing optional dates, descriptions, and images do not reject the post. For sites that add images shortly after publication, a later refresh updates the same saved item when its identity is unchanged; it does not require a duplicate post. Refreshes follow the configured interval, or can be requested manually.

History is stored in the selected SQLite or PostgreSQL database and survives restarts. It retains the latest **50 runs per feed**, with no age expiry; deleting a feed deletes its runs. Preview traces are not saved. Each stored trace keeps at most two fetch attempts and 20 warnings per attempt, reports omitted warnings, and is capped at 64 KiB. Field samples are short, rendered as plain text, and have absolute URLs and recognizable credentials redacted. Source response bodies, request headers, cookies, and reader links are not captured in traces. Samples may still contain text from the source page, so treat diagnostic history and database backups as private.

## Backup and restore

Recipe [export/import](recipe-portability.md) moves configurations. A database backup also preserves saved items, publication dates, GUIDs, reader tokens, schedules, and run history.

The utility and commands below are for SQLite. For PostgreSQL, use the [pg_dump and pg_restore procedure](postgresql.md#backup-and-restore); a copy of `/data/rss.db` does not back up PostgreSQL data.

Use local disk for SQLite. A running database may have committed data in `rss.db-wal`; copying `rss.db` alone is unsafe. The [backup utility](../scripts/backup.py) supports a host directory or named volume mounted at `/data`, with the container's `DATA_DIR=/data`. Run it on the Docker host with Python 3.9+, its `sqlite3` module, and Docker access. Host-directory restore also requires root to assign the application UID/GID 65532. PostgreSQL host directories still require `pg_dump`; this utility refuses a PostgreSQL-configured container.

### Create and verify a backup

```sh
sudo install -d -m 700 /var/backups/rss-workshop
sudo python3 scripts/backup.py backup \
  --container rss-workshop-rss-workshop-1 \
  --output /var/backups/rss-workshop/before-upgrade
sudo python3 scripts/backup.py verify /var/backups/rss-workshop/before-upgrade
docker compose exec -T rss-workshop /rss-workshop -healthcheck
```

Confirm the actual container name with your deployment's Compose `ps` command and choose a new output directory for each backup. The utility refuses existing output directories and concurrent backups of the same container. It also refuses storage that overlaps another running container's mounts, including a parent directory or a named volume exposed through a bind mount. Stop any native processes using the same files too; Docker inspection cannot identify those writers. Do not use a remote Docker context for host-directory operations.

Backup briefly stops the selected app, streams `/data` using [Docker's stopped-container copy support](https://docs.docker.com/reference/cli/docker/container/cp/) and keeps only `rss.db` and its write-ahead log, then restarts the app before validating the copy. [Automatic pre-upgrade copies](#automatic-copy-before-an-sqlite-upgrade) in `/data/backups` pass through the stream without being saved, but still add to the time the app is stopped. Readers and the UI are unavailable during the copy; restarting ends login sessions. A previously stopped container remains stopped. Do not start or recreate the app during this operation. After an error or interruption, check readiness; the utility attempts to restart an originally running app even if copying fails.

The copied database and WAL are consolidated with the [SQLite backup API](https://docs.python.org/3/library/sqlite3.html#sqlite3.Connection.backup). The output contains a standalone `rss.db` and `manifest.json`. Verification checks SHA-256, SQLite integrity, foreign keys, a supported schema version, and feed/item/run counts, plus library filter and filter-link counts for schema 5. Files use mode 0600 and the backup directory uses 0700.

Store an off-host copy in protected backup storage. Backups contain private URLs, content, and working reader tokens. Keep `.env`, Compose configuration, and the selected image digest separately. Allow space for the copied database, consolidated snapshot, and output; set the host's `TMPDIR` if needed. Checksums detect corruption, not replacement of both the database and manifest.

### Restore into a new host directory

Choose a new directory under an existing parent. The default Compose data location is `./data`, controlled by `RSS_DATA_DIR`; restoration refuses **all existing destinations**, including empty directories and symlinks. Do not pre-create the destination itself:

```sh
sudo install -d -m 700 /srv/rss-workshop
sudo python3 scripts/backup.py restore /var/backups/rss-workshop/before-upgrade \
  --directory /srv/rss-workshop/data-restored
```

The restore command verifies the backup before creating anything, copies the database, checks its checksum and integrity, and sets UID/GID 65532 ownership with mode 0700 on the directory and 0600 on `rss.db`. Parent directories must exist without symlink components. No Docker image is needed, and no container is started or changed. A failed restore leaves only its newly created directory for inspection; the backup remains untouched.

Stop the current app using its normal Compose files, then edit the existing private `.env`:

```dotenv
RSS_DATA_DIR=/srv/rss-workshop/data-restored
```

Set `RSS_IMAGE` to the saved compatible image digest before recreating the service. Use the same project, runtime and networking options, and keep the already-local image during recovery:

```sh
docker compose up -d --no-build --pull never --wait
docker compose exec -T rss-workshop /rss-workshop -healthcheck
```

Sign in and check the saved feeds and RSS/Atom output. Preserve the previous storage and image until verification succeeds. To roll back from another host directory, stop the app, restore the old `RSS_DATA_DIR` setting, and recreate it. An older backup restores old tokens and schedules too; reset reader links if needed.

### Move an existing SQLite volume to a host directory

Upgrading the Compose file does not copy a named volume into `./data`. Confirm the current container and volume before recreating anything; the default older volume was `rss-workshop_rss-data`, but project names can change it:

```sh
docker compose ps
docker inspect --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Type}} {{.Name}}{{end}}{{end}}' \
  rss-workshop-rss-workshop-1
```

Keep the original volume name, image, and Compose options for rollback. If `./data` already exists, use a different new directory; do not empty or overwrite it. Stop the app before taking the final migration backup so no changes occur after the snapshot:

```sh
docker stop --time 20 rss-workshop-rss-workshop-1
sudo install -d -m 700 /var/backups/rss-workshop
sudo python3 scripts/backup.py backup \
  --container rss-workshop-rss-workshop-1 \
  --output /var/backups/rss-workshop/before-bind-migration
sudo python3 scripts/backup.py verify /var/backups/rss-workshop/before-bind-migration
sudo install -d -m 700 /srv/rss-workshop
sudo python3 scripts/backup.py restore /var/backups/rss-workshop/before-bind-migration \
  --directory /srv/rss-workshop/data-from-volume
```

The backup utility leaves that stopped app stopped. Pin `RSS_IMAGE` to the saved compatible image digest and use `--pull never` during this storage move. Set `RSS_DATA_DIR=/srv/rss-workshop/data-from-volume` in the existing `.env`, then recreate the app using the same Compose project and runtime options. Verify readiness, saved feeds, and RSS/Atom output. The original named volume remains unchanged; never remove it as part of this migration.

For rollback to that original volume, stop the app and use the explicit named-volume override below, replacing its external volume name with the original name. A top-level volume declaration alone does **not** replace the new bind mount. To migrate PostgreSQL storage, use its [dump/restore procedure](postgresql.md#backup-and-restore) with a fresh `POSTGRES_DATA_DIR`; never copy a live PostgreSQL directory.

### Restore or select a named volume

Named-volume restore remains available for existing deployments and accepts backups from either mount type. Use an unused name and the already-local compatible image from your deployment; replace the example image below with your saved tag or digest:

```sh
sudo python3 scripts/backup.py restore /var/backups/rss-workshop/before-upgrade \
  --volume rss-workshop-restored \
  --image ghcr.io/ldogg123/rss-workshop:v1.1.1-browser
```

This verifies the copy with UID/GID 65532 ownership, never pulls an image or starts the app, and refuses an existing volume. Its temporary container is removed; a failed restore leaves the new volume for inspection. Manifest format 1 is unchanged; backups of every schema the utility supports, including [automatic pre-upgrade copies](#automatic-copy-before-an-sqlite-upgrade), remain usable with the current utility. Select an app version that supports the restored database schema.

Create an ignored local `compose.restore.yaml` to explicitly replace the app's `/data` bind mount. For rollback, use the preserved original volume's actual name instead of `rss-workshop-restored`:

```yaml
services:
  rss-workshop:
    volumes: !override
      - type: volume
        source: restored-data
        target: /data
volumes:
  restored-data:
    external: true
    name: rss-workshop-restored
```

Include any additional app mounts from your own configuration when replacing the mount list. Pin `RSS_IMAGE` to the saved compatible image digest, stop the current app, then add this storage override to your normal Compose files:

```sh
docker compose -f compose.yaml -f compose.restore.yaml up -d --no-build --pull never --wait
docker compose -f compose.yaml -f compose.restore.yaml exec -T rss-workshop /rss-workshop -healthcheck
```

The default image includes Chromium. For static-only operation, keep `-f compose.static.yaml` after the base file and before the storage override. An explicit `RSS_IMAGE` must match the runtime. Keep the same files and saved image for later operations. Never run two app processes against one database, and never use `docker compose down -v` to perform a storage switch.

For native installations, stop the process and copy `rss.db` with its WAL/SHM files from `DATA_DIR` into a new private directory before restarting; the `backups` subdirectory holds earlier automatic copies and can be left out. A live backup must use a SQLite backup API or an administrator-managed `sqlite3 .backup` command.

## Upgrades

Back up the database and retain the current configuration and image digest. Update your checkout of this repository first (`git pull`): the Compose files pass new settings such as `UPGRADE_BACKUP` into the container only in their current form, and an older `scripts/backup.py` cannot read a newer schema. With `RSS_IMAGE` blank, the current Compose files follow the latest stable release through `latest` (browser) or `latest-static`. Existing explicit `.env` values continue to take precedence: clear `RSS_IMAGE` to follow the default, or set a chosen versioned tag/digest to stay pinned. Older checkouts with versioned defaults can select `RSS_IMAGE=ghcr.io/ldogg123/rss-workshop:latest` explicitly, or `:latest-static` with their static runtime. With PostgreSQL, check that `DATABASE_URL` percent-encodes its user name, password and query values as [PostgreSQL setup](postgresql.md#existing-postgresql-server) describes; the [changelog](../CHANGELOG.md) names each release's upgrade steps.

Pull and recreate the app using the same Compose files, database and host directories:

```sh
docker compose up -d --pull always --wait
docker compose exec -T rss-workshop /rss-workshop -healthcheck
```

Include all overrides used by your deployment. `latest` does not update a running container by itself, and `docker compose restart` does not switch its image. The update command checks the registry and recreates the app when its image changes. For local source builds, use the matching [development build override](deployment.md#build-container-images-from-source) and `up -d --build --wait`. Check a saved feed and its RSS/Atom output afterward.

### Database compatibility

Direct upgrades are supported from every released SQLite or PostgreSQL app schema. Intermediate app versions do not need to be installed:

| Existing app release | Stored schema | Startup upgrade to the current app |
| --- | --- | --- |
| v0.1.0, v0.1.1 | 1 | 1 → 2 → 3 → 4 → 5 in one transaction |
| v0.1.2 | 2 | 2 → 3 → 4 → 5 in one transaction |
| v0.2.0 | 3 | 3 → 4 → 5 in one transaction |
| v1.0.0 | 4 | 4 → 5 in one transaction |
| v1.1.0 | 5 | None; opened as it is |

Schema 2 adds diagnostic storage; schema 3 protects stored filter rules from older binaries that would ignore them; schema 4 stores fetched article bodies beside each story's list-page description; schema 5 adds the filter library, and prevents older binaries from refreshing feeds without the library filters they use. **An installation older than v1.1.0 migrates when it starts this version, so take a backup first**: once upgraded, an older release will refuse to open the database. SQLite databases also get an [automatic copy](#automatic-copy-before-an-sqlite-upgrade) first. A v1.1.0 database is already at schema 5, so it is opened unchanged, without a copy, and v1.1.0 can still open it. The upgrade preserves feeds, reader tokens, saved items, GUIDs, publication dates and run history. A failed migration rolls back the entire upgrade. Unknown or newer schema versions are refused rather than rewritten. Switching `DATABASE_URL` between SQLite and PostgreSQL does not migrate data between backends.

Released migrations and frozen database fixtures remain in the project as new versions are added, with tests for direct upgrades and rollback. Back up before each upgrade: the SQLite utility accepts schemas 1 through 5, verifies the copied data, and restores its original schema without changing it. PostgreSQL users retain a verified [logical dump](postgresql.md#backup-and-restore).

### Automatic copy before an SQLite upgrade

Before it upgrades an older SQLite schema, the app saves a copy of the database, still at its original schema, to `backups/pre-upgrade-schema-<N>-<UTC time>` inside the data directory: the `backups` directory under your `RSS_DATA_DIR` (default `./data`) with Compose, `/data/backups` inside the container, or under `DATA_DIR` for a native executable. The startup log names the copy. The copy is a consistent snapshot, including changes still in the write-ahead log. It uses the same `rss.db` and `manifest.json` format as the [backup utility](#create-and-verify-a-backup), and passes the same checks before the upgrade starts: integrity, foreign keys, schema version, row counts, and SHA-256. If the copy cannot be saved or verified, or the data directory lacks the free space it needs, the app stops before upgrading the database and explains why. Before copying, it removes stories, runs and filter links left behind by feeds that were already deleted: earlier releases could delete a feed without them, and they would otherwise fail the copy's foreign key check.

The copy is private data like any other backup, and belongs to UID/GID 65532, so use `sudo` to read it on the host. Replace `./data` below with your `RSS_DATA_DIR` and the name with the one in your log:

```sh
sudo ls ./data/backups
sudo python3 scripts/backup.py verify ./data/backups/pre-upgrade-schema-4-20260916T120000Z
```

It covers a failed or unwanted upgrade on this host, not a lost disk: it lives on the same storage as the database, and the backup utility saves only the live database, not these copies. The app never deletes a completed copy. Remove one once the upgraded version has been working and you have an off-host backup. If the upgrade fails and the app is restarted, an identical earlier copy is reused rather than saved again; a copy interrupted part-way, for example by stopping the container, is left in a hidden `.pre-upgrade-partial-*` directory and removed at the next start.

The copy needs free space roughly equal to the database. If the data directory cannot hold it, free space and start again. Otherwise stop the app, which Compose would keep restarting (`docker compose stop rss-workshop`), take a backup elsewhere with the utility, set `UPGRADE_BACKUP=false` for one start, then remove the setting so later upgrades are copied again. It only skips the copy, which is logged as a warning; the upgrade itself is unchanged. PostgreSQL deployments get no automatic copy, because the app cannot run `pg_dump`: it logs a warning when it upgrades, so take a [logical dump](postgresql.md#backup-and-restore) first.

For a downgrade, stop the app and restore its pre-upgrade backup, or the automatic copy, into separate storage with `scripts/backup.py restore`, then select the matching old image digest or executable. Older apps cannot open a newer schema than they support. Keep the original storage until recovery is verified; there is no in-place schema downgrade. Storage-layout changes, such as [moving an old named volume to a host directory](#move-an-existing-sqlite-volume-to-a-host-directory), are separate from schema upgrades.

## Scheduling and limits

Reader requests serialize saved data without fetching sources. Failed or empty extraction preserves the last successful output. Editing a recipe invalidates source validators and in-flight results. Due jobs remain in the selected database, with bounded refresh slots, one active refresh per feed, and error backoff capped at 24 hours. FlareSolverr feeds start only while a [FlareSolverr slot](flaresolverr.md#capacity) is free; the others stay due and start oldest first, without failing or backing off for the wait.

Source bodies are limited to 4 MiB, both as received and once decoded to UTF-8, and extraction to 1,000 cards, 256 KiB of HTML per item, and 8 MiB of total extracted title/content per job. The default retention is 500 items and 50 run records per feed. Disappeared items remain until count pruning; age retention is not supported. Parsing and selector evaluation are synchronous, so pathological selectors can exceed the network deadline.

### Pausing and resuming

**Pause** on a feed card stops its automatic refreshes and **Resume** restarts them. Nothing else about the feed changes: its recipe, error message, failure count and the source's conditional-request validators stay as they were, a refresh already running is still saved, and readers keep receiving the same output, so a paused feed goes on serving its saved stories. Resuming makes the feed due at once, even during an error backoff. Its failure count is kept, so if it fails again the backoff continues where it left off instead of starting over at 2 minutes.

The editor's **Refresh automatically** checkbox is saved with the rest of the feed, and a save, like any edit, clears the error, failure count and validators and discards a running refresh. If you leave the checkbox as it was when the editor opened, the save keeps the feed's current setting, so a feed you paused or resumed from its card while the editor was open stays that way.

The card uses `POST /api/feeds/{id}/enabled` with `{"enabled": false}` or `{"enabled": true}`; it needs a signed-in session and passes the same Origin and CSRF checks as every other change. Feed saves accept an optional top-level `loaded_enabled`, the `enabled` value the editor loaded: when the two are equal, the save keeps the stored setting. A save without it sets `enabled` as sent, so a script that pauses or resumes a feed by sending its object from `GET /api/feeds` back with a different `enabled` keeps working: saves accept and ignore the state the listing reports, such as `count`, `version`, `held`, `quiet` and `last_changed`.

### Waiting to retry after a failed save

When the database refuses a finished refresh, for example because its disk is full, nothing from that refresh is stored, including the feed's next refresh time. The app therefore holds the feed in memory, or it would fetch the source again every second until the database recovered: 2 minutes after the first such failure, then twice as long after each further one, up to 1,024 minutes (about 17 hours). The log reports each one as `refresh not saved`, the feed's card shows **Waiting to retry**, `GET /api/feeds` lists it with `"held": true`, and `rss_workshop_feeds_held` counts it.

Once the database accepts writes again, **Refresh** on the card, saving the feed in the editor, or resuming it tries again at once; otherwise the feed waits out its hold. A retry that fails again waits twice as long as the one before, because the count is kept until a refresh is stored. Restarting the app clears every hold.

## Optional VPN routing

See [VPN networking with Gluetun](gluetun.md) for the supported Compose override, port publishing, database routing, and verification limits. Keep the same networking files when backing up or switching storage.
