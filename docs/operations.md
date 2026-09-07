# Operations

## Health and logs

`GET /healthz` checks liveness; `GET /readyz` checks the selected database connection. The container uses `/rss-workshop -healthcheck` for readiness. Use the same Compose files and project options as your deployment:

```sh
docker compose -f compose.yaml -f compose.image.yaml ps
docker compose -f compose.yaml -f compose.image.yaml exec -T rss-workshop /rss-workshop -healthcheck
docker compose -f compose.yaml -f compose.image.yaml logs --tail 30 rss-workshop
```

The dashboard reports saved items, refresh status, due feeds, and fetch capacity. Chromium/FlareSolverr capacity indicates configuration, not remote-site health. Refresh logs include the internal feed ID, HTTP status, duration, item count, and success flag; source URLs and reader tokens are omitted.

These examples use the published image selected by `RSS_IMAGE` in `.env`. Keep any static, PostgreSQL, VPN, or local overrides before the final `compose.image.yaml`. Local source builds omit the image override.

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
docker compose -f compose.yaml -f compose.image.yaml exec -T rss-workshop /rss-workshop -healthcheck
```

Confirm the actual container name with your deployment's Compose `ps` command and choose a new output directory for each backup. The utility refuses existing output directories and concurrent backups of the same container. It also refuses storage that overlaps another running container's mounts, including a parent directory or a named volume exposed through a bind mount. Stop any native processes using the same files too; Docker inspection cannot identify those writers. Do not use a remote Docker context for host-directory operations.

Backup briefly stops the selected app, copies `/data` using [Docker's stopped-container copy support](https://docs.docker.com/reference/cli/docker/container/cp/), and restarts it before validating the copy. Readers and the UI are unavailable during the copy; restarting ends login sessions. A previously stopped container remains stopped. Do not start or recreate the app during this operation. After an error or interruption, check readiness; the utility attempts to restart an originally running app even if copying fails.

The copied database and WAL are consolidated with the [SQLite backup API](https://docs.python.org/3/library/sqlite3.html#sqlite3.Connection.backup). The output contains a standalone `rss.db` and `manifest.json`. Verification checks SHA-256, SQLite integrity, foreign keys, schema version 1, and feed/item/run counts. Files use mode 0600 and the backup directory uses 0700.

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

Recreate the service using the same project, runtime, networking, and image options as before:

```sh
docker compose -f compose.yaml -f compose.image.yaml up -d --no-build --wait
docker compose -f compose.yaml -f compose.image.yaml exec -T rss-workshop /rss-workshop -healthcheck
```

Sign in and check the saved feeds and RSS/Atom output. Preserve the previous storage and image until verification succeeds. To roll back from another host directory, stop the app, restore the old `RSS_DATA_DIR` setting, and recreate it. An older backup restores old tokens and schedules too; reset reader links if needed.

### Move an existing SQLite volume to a host directory

Upgrading the Compose file does not copy a named volume into `./data`. Confirm the current container and volume before recreating anything; the default older volume was `rss-workshop_rss-data`, but project names can change it:

```sh
docker compose -f compose.yaml -f compose.image.yaml ps
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

The backup utility leaves that stopped app stopped. Set `RSS_DATA_DIR=/srv/rss-workshop/data-from-volume` in the existing `.env`, then recreate the app using the same Compose project and runtime options. Verify readiness, saved feeds, and RSS/Atom output. The original named volume remains unchanged; never remove it as part of this migration.

For rollback to that original volume, stop the app and use the explicit named-volume override below, replacing its external volume name with the original name. A top-level volume declaration alone does **not** replace the new bind mount. To migrate PostgreSQL storage, use its [dump/restore procedure](postgresql.md#backup-and-restore) with a fresh `POSTGRES_DATA_DIR`; never copy a live PostgreSQL directory.

### Restore or select a named volume

Named-volume restore remains available for existing deployments and accepts backups from either mount type. Use an unused name and the already-local compatible image from your deployment; replace the example image below with your saved tag or digest:

```sh
sudo python3 scripts/backup.py restore /var/backups/rss-workshop/before-upgrade \
  --volume rss-workshop-restored \
  --image ghcr.io/ldogg123/rss-workshop:v0.1.0-browser
```

This verifies the copy with UID/GID 65532 ownership, never pulls an image or starts the app, and refuses an existing volume. Its temporary container is removed; a failed restore leaves the new volume for inspection. The backup format is unchanged, so earlier version-1 backups remain usable.

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

Include any additional app mounts from your own configuration when replacing the mount list. Stop the current app, then insert this storage override before the final image override:

```sh
docker compose -f compose.yaml -f compose.restore.yaml -f compose.image.yaml up -d --no-build --wait
docker compose -f compose.yaml -f compose.restore.yaml -f compose.image.yaml exec -T rss-workshop /rss-workshop -healthcheck
```

The default image includes Chromium. For static-only operation, keep your `-static` `RSS_IMAGE` paired with `-f compose.static.yaml` after the base file. Keep the same files for later operations. Never run two app processes against one database, and never use `docker compose down -v` to perform a storage switch.

For native installations, stop the process and copy the entire `DATA_DIR`, including WAL/SHM files, into a new private directory before restarting. A live backup must use a SQLite backup API or an administrator-managed `sqlite3 .backup` command.

## Upgrades

Back up the data and retain the current configuration and image digest. Set `RSS_IMAGE` in `.env` to the intended release tag or immutable digest, keeping the same browser/static variant, then pull and recreate the app:

```sh
docker compose -f compose.yaml -f compose.image.yaml up -d --pull always --wait
docker compose -f compose.yaml -f compose.image.yaml exec -T rss-workshop /rss-workshop -healthcheck
```

Include all overrides used by your deployment, with `compose.image.yaml` last. For local source builds, omit the image override and use `up -d --build --wait`. Check a saved feed afterward; see [deployment](deployment.md).

The app supports schema version 1 and rejects unsupported versions. There is no automatic rollback or in-place schema downgrade. A downgrade across schema versions requires a compatible database backup. Review the backup tool alongside any schema migration.

## Scheduling and limits

Reader requests serialize saved data without fetching sources. Failed or empty extraction preserves the last successful output. Editing a recipe invalidates source validators and in-flight results. Due jobs remain in the selected database, with bounded refresh slots, one active refresh per feed, and error backoff capped at 24 hours.

Source bodies are limited to 4 MiB and extraction to 1,000 cards, 256 KiB of HTML per item, and 8 MiB of total extracted title/content per job. The default retention is 500 items and 50 run records per feed. Disappeared items remain until count pruning; age retention is not supported. Parsing and selector evaluation are synchronous, so pathological selectors can exceed the network deadline.

## Optional VPN routing

See [VPN networking with Gluetun](gluetun.md) for the supported Compose override, port publishing, database routing, and verification limits. Keep the same networking files when backing up or switching storage.
