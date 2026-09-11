# Operations

## Health and logs

`GET /healthz` checks liveness; `GET /readyz` checks the selected database connection. The container uses `/rss-workshop -healthcheck` for readiness. Use the same Compose files and project options as your deployment:

```sh
docker compose ps
docker compose exec -T rss-workshop /rss-workshop -healthcheck
docker compose logs --tail 30 rss-workshop
```

The dashboard reports saved items, refresh status, due feeds, and fetch capacity. Chromium/FlareSolverr capacity indicates configuration, not remote-site health.

### Log detail

`LOG_LEVEL` selects how much reaches `docker compose logs`:

| Level | Reports |
| --- | --- |
| `debug` | Everything below, plus scheduler activity when feeds are due |
| `info` (default) | Startup configuration, sign-ins, shutdown, and each completed refresh |
| `warn` | Only problems: failed refreshes with their reason, and rejected sign-ins |
| `error` | Only failures: the server stopping, and the database errors that abort a scheduler tick or discard a completed refresh |

The startup line summarizes the running deployment — version, commit, database backend, worker count, whether Chromium, FlareSolverr and metrics are enabled — which is the quickest way to confirm a container is running the configuration you intended.

A failed refresh logs the feed title, its consecutive failure count, and the reason; a successful one logs the item count, HTTP status, and duration. Feed titles appear because they are your own labels, but source URLs, reader links, passwords and session cookies are never logged. A run of `login rejected` lines with a remote address is worth investigating.

`LOG_FORMAT=json` emits one JSON object per line for a log collector such as Loki or Elasticsearch. The default `text` stays easier to read directly.

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
| `rss_workshop_feed_failures` | Consecutive failures for one feed |
| `rss_workshop_feed_last_success_timestamp_seconds` | Alert when a feed has not succeeded for too long; `0` means it never has |
| `rss_workshop_feed_items` | Stories saved for one feed |
| `rss_workshop_refresh_active` / `_capacity` | Whether refreshes are queueing |

Because the series name your feeds, the token is a real credential: keep it out of shared dashboards and rotate it by changing `METRICS_TOKEN` and recreating the service. Metrics never include reader links, source URLs, or story content. A library beyond 1,000 feeds keeps every aggregate, but per-feed series cover only the first 1,000 feeds by title, so a scrape stays bounded. Feeds after that have no per-feed series; `rss_workshop_feeds` still counts them.

These examples use the default published browser image, or the matching image selected by `RSS_IMAGE` in `.env`. Include any static, PostgreSQL, VPN, storage, or development build overrides used by your deployment.

### Feed diagnostics

Choose **Diagnostics** on a saved feed to see its latest refresh results, newest first. Expand a run for the requested fetch mode, recipe version, source HTTP status, duration, response size, matched elements, valid stories, included/filtered counts, and field warnings. Auto records both static and Chromium attempts when it falls back, so the initial matching failure remains visible. A failed request without an HTTP response shows no recorded status; a `304` means the source reported no changes and saved items were kept.

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

Backup briefly stops the selected app, copies `/data` using [Docker's stopped-container copy support](https://docs.docker.com/reference/cli/docker/container/cp/), and restarts it before validating the copy. Readers and the UI are unavailable during the copy; restarting ends login sessions. A previously stopped container remains stopped. Do not start or recreate the app during this operation. After an error or interruption, check readiness; the utility attempts to restart an originally running app even if copying fails.

The copied database and WAL are consolidated with the [SQLite backup API](https://docs.python.org/3/library/sqlite3.html#sqlite3.Connection.backup). The output contains a standalone `rss.db` and `manifest.json`. Verification checks SHA-256, SQLite integrity, foreign keys, schema version 1, 2, or 3, and feed/item/run counts. Files use mode 0600 and the backup directory uses 0700.

Store an off-host copy in protected backup storage. Backups contain private URLs, content, and working reader tokens. Keep `.env`, Compose configuration, and the selected image digest separately. Allow space for the stopped copy, consolidated snapshot, and output; set the host's `TMPDIR` if needed. Checksums detect corruption, not replacement of both the database and manifest.

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
  --image ghcr.io/ldogg123/rss-workshop:v0.2.0-browser
```

This verifies the copy with UID/GID 65532 ownership, never pulls an image or starts the app, and refuses an existing volume. Its temporary container is removed; a failed restore leaves the new volume for inspection. Manifest format 1 is unchanged; schema-1, schema-2, and schema-3 backups remain usable with the current utility. Select an app version that supports the restored database schema.

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

For native installations, stop the process and copy the entire `DATA_DIR`, including WAL/SHM files, into a new private directory before restarting. A live backup must use a SQLite backup API or an administrator-managed `sqlite3 .backup` command.

## Upgrades

Back up the database and retain the current configuration and image digest. With `RSS_IMAGE` blank, the current Compose files follow the latest stable release through `latest` (browser) or `latest-static`. Existing explicit `.env` values continue to take precedence: clear `RSS_IMAGE` to follow the default, or set a chosen versioned tag/digest to stay pinned. Older checkouts with versioned defaults can select `RSS_IMAGE=ghcr.io/ldogg123/rss-workshop:latest` explicitly, or `:latest-static` with their static runtime.

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
| v0.1.0, v0.1.1 | 1 | 1 → 2 → 3 in one transaction |
| v0.1.2 | 2 | 2 → 3 in one transaction |
| v0.2.0 | 3 | Already current |

Schema 2 adds diagnostic storage; schema 3 protects stored filter rules from older binaries that would ignore them. The upgrade preserves feeds, reader tokens, saved items, GUIDs, publication dates and run history. A failed migration rolls back the entire upgrade. Unknown or newer schema versions are refused rather than rewritten. Switching `DATABASE_URL` between SQLite and PostgreSQL does not migrate data between backends.

Released migrations and frozen database fixtures remain in the project as new versions are added, with tests for direct upgrades and rollback. Back up before each upgrade: the SQLite utility accepts schemas 1, 2 and 3, verifies the copied data, and restores its original schema without changing it. PostgreSQL users retain a verified [logical dump](postgresql.md#backup-and-restore).

For a downgrade, stop the app and restore its pre-upgrade backup into separate storage, then select the matching old image digest or executable. Older apps cannot open a newer schema than they support. Keep the original storage until recovery is verified; there is no in-place schema downgrade. Storage-layout changes, such as [moving an old named volume to a host directory](#move-an-existing-sqlite-volume-to-a-host-directory), are separate from schema upgrades.

## Scheduling and limits

Reader requests serialize saved data without fetching sources. Failed or empty extraction preserves the last successful output. Editing a recipe invalidates source validators and in-flight results. Due jobs remain in the selected database, with bounded refresh slots, one active refresh per feed, and error backoff capped at 24 hours.

Source bodies are limited to 4 MiB and extraction to 1,000 cards, 256 KiB of HTML per item, and 8 MiB of total extracted title/content per job. The default retention is 500 items and 50 run records per feed. Disappeared items remain until count pruning; age retention is not supported. Parsing and selector evaluation are synchronous, so pathological selectors can exceed the network deadline.

## Optional VPN routing

See [VPN networking with Gluetun](gluetun.md) for the supported Compose override, port publishing, database routing, and verification limits. Keep the same networking files when backing up or switching storage.
