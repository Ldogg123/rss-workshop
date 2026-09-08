# PostgreSQL

SQLite remains the default. Set `DATABASE_URL` to an explicit `postgres://` or `postgresql://` connection URI to use PostgreSQL with either app runtime. Keep one app process per database.

Selecting another database does not migrate or merge data. A new database starts with an empty library; the existing SQLite file or PostgreSQL database is left separate. Recipe [export/import](recipe-portability.md) can move configurations as paused copies, but does not preserve articles or reader links.

Run the commands below from the RSS Workshop checkout root. With `RSS_IMAGE` blank or unset, the default Compose installation pulls `ghcr.io/ldogg123/rss-workshop:v0.1.1-browser`, which includes Chromium. Prepare the app configuration before adding PostgreSQL:

```sh
if [ ! -e .env ]; then
  cp .env.example .env
fi
chmod 600 .env
# Edit .env: set ADMIN_PASSWORD to your chosen admin password.
sudo mkdir -p -m 700 ./data
sudo chown 65532:65532 ./data
sudo chmod 700 ./data
```

Keep an existing `.env` and its configured admin password or `ADMIN_PASSWORD_HASH`. The base app mount still requires the host directory selected by `RSS_DATA_DIR`, even when PostgreSQL is selected; use that path instead of `./data` if customized. Add the database settings below before starting the app. For native operation, export the same authentication and database variables instead; see [source setup](deployment.md#run-from-source).

## Existing PostgreSQL server

Create a dedicated UTF-8 database and non-superuser login role. For example, an administrator can run these commands in `psql`; `\password` prompts without putting the password in the command text:

```text
CREATE ROLE rss_workshop LOGIN;
\password rss_workshop
CREATE DATABASE rss_workshop OWNER rss_workshop TEMPLATE template0 ENCODING 'UTF8';
```

The app creates its tables and indexes in the selected database. Use a database reserved for RSS Workshop and a role that owns it. RSS Workshop requires `server_encoding=UTF8` and forces `client_encoding=UTF8`, including when the URI or environment requests another client encoding. It refuses an incompatible database before initializing tables. PostgreSQL documents these [database and client encodings](https://www.postgresql.org/docs/18/multibyte.html). Set the connection URI in the private `.env` file for Compose, or export it for native operation:

```dotenv
DATABASE_URL='postgresql://rss_workshop:ENCODED_PASSWORD@db.example.net:5432/rss_workshop?sslmode=verify-full'
```

Replace the host, database, and password with your values. Percent-encode reserved characters in URI credentials, including `@`, `:`, `/`, `%`, `?`, and `#`; PostgreSQL documents [connection URI encoding](https://www.postgresql.org/docs/18/libpq-connect.html#LIBPQ-CONNSTRING). Single quotes keep literal dollar signs from being expanded by Compose. A randomly generated hexadecimal password needs no URI encoding.

Use verified TLS for a remote server. If it uses a private certificate authority, mount its CA certificate read-only into the app and add the appropriate `sslrootcert` path to the URI. Database access uses ordinary administrator-configured networking; `ALLOW_CIDRS` governs scraped source pages and does not authorize database connections. Check the server's access rules and firewall separately.

Start or recreate the app. Do not include the PostgreSQL override when connecting to an existing server:

```sh
docker compose up -d --pull always --wait
```

## New PostgreSQL container

The optional [Compose override](../compose.postgres.yaml) uses the Docker Official Image `postgres:18-alpine`. `POSTGRES_DATA_DIR` selects its host directory, defaulting to `./postgres-data`, mounted at `/var/lib/postgresql`, the [PostgreSQL 18 image's data location](https://hub.docker.com/_/postgres). The database has no published host port. The app waits for PostgreSQL's health check before starting.

For a new database, prepare the host directory before startup. Use the path configured in `POSTGRES_DATA_DIR` if it differs from the default:

```sh
sudo install -d -m 755 ./postgres-data
```

The mounted parent needs mode 0755 so the image's PostgreSQL user can traverse it. The official entrypoint manages ownership and mode 0700 on the actual database directory below it. With PostgreSQL 18, the cluster and server configuration files, including `postgresql.conf` and `pg_hba.conf`, live under `POSTGRES_DATA_DIR/18/docker` on the host. RSS Workshop's own connection and application settings remain in `.env`.

Compose refuses a missing host directory. Relative paths resolve from the directory containing `compose.yaml`; use an absolute path to keep storage elsewhere. Keep custom data paths outside the checkout, or exclude them from both Git and Docker builds. Existing named-volume databases need the [migration procedure below](#existing-postgresql-named-volumes).

Generate a unique password, for example:

```sh
python3 -c 'import secrets; print(secrets.token_hex(24))'
```

Copy the same generated value into both settings in `.env`:

```dotenv
POSTGRES_PASSWORD='YOUR_GENERATED_HEX_PASSWORD'
DATABASE_URL='postgres://rss_workshop:YOUR_GENERATED_HEX_PASSWORD@postgres:5432/rss_workshop?sslmode=disable'
```

Both variables are required by the override; there is no default password. `DATABASE_URL` is explicit so arbitrary passwords are not accidentally inserted into an invalid URI. `sslmode=disable` here is for this database's private Docker network, rather than a remote database connection.

```sh
docker compose -f compose.yaml -f compose.postgres.yaml up -d --pull always --wait
```

For the lightweight runtime, add `-f compose.static.yaml` immediately after the base file. With `RSS_IMAGE` blank or unset, that override pulls `ghcr.io/ldogg123/rss-workshop:v0.1.1-static`. An explicit `RSS_IMAGE` must match the selected browser or static runtime. Use the same overrides for later operations. For a local source build, follow [build container images from source](deployment.md#build-container-images-from-source) and retain the PostgreSQL override.

The official image's `POSTGRES_USER=rss_workshop` creates the bootstrap administrator for this dedicated container. Use the non-superuser setup above on a shared server. Initialization variables apply only to an empty data directory: changing `POSTGRES_PASSWORD` in `.env` does not change an existing database password. Rotate it in PostgreSQL and update `DATABASE_URL` together.

Keep the same host PostgreSQL directory on later runs. The app's `RSS_DATA_DIR` directory remains mounted at `/data` but is unused while `DATABASE_URL` selects PostgreSQL. To return to SQLite, clear `DATABASE_URL` and omit `compose.postgres.yaml` from the app's Compose command; keep the PostgreSQL directory for recovery. This does not copy PostgreSQL data back. Back up before changing backend or upgrading PostgreSQL. A PostgreSQL major-version upgrade needs its own supported database migration procedure; changing the image tag alone is insufficient.

### Existing PostgreSQL named volumes

Changing `POSTGRES_DATA_DIR` does not copy an existing server's data. First stop RSS Workshop to prevent further writes, leaving the old PostgreSQL server running on its original volume. Keep the app stopped while creating and verifying a final logical dump using the [backup procedure](#backup-and-restore). Save the current image and Compose configuration, then stop PostgreSQL. Prepare a new, empty host directory and set `POSTGRES_DATA_DIR` to it.

Start only the database with the new bind mount, keeping the app stopped:

```sh
docker compose -f compose.yaml -f compose.postgres.yaml up -d --no-deps --wait postgres
```

Follow the restore procedure below to restore into a new database and verify the saved records before starting the app with that database selected. Keep the original named volume and configuration for rollback. Do not copy PostgreSQL's database files while it is running. For an existing SQLite volume, use the separate [SQLite storage migration](operations.md#move-an-existing-sqlite-volume-to-a-host-directory).

## Backup and restore

Use PostgreSQL's `pg_dump` and `pg_restore`, not the SQLite backup utility or a copy of the app's `/data` directory. Use client tools compatible with your server, normally the same PostgreSQL major version. A [logical dump captures a consistent database snapshot](https://www.postgresql.org/docs/18/backup-dump.html) while the app remains running.

For the supplied Compose server, create a custom-format dump in a private backup directory. Keep any static or VPN overrides used by your deployment:

```sh
mkdir -p backups
chmod 700 backups
umask 077
docker compose -f compose.yaml -f compose.postgres.yaml exec -T postgres pg_dump \
  -U rss_workshop -d rss_workshop --format=custom --no-owner --no-privileges \
  > backups/postgres-before-upgrade.dump
docker compose -f compose.yaml -f compose.postgres.yaml exec -T postgres pg_restore \
  --list < backups/postgres-before-upgrade.dump > /dev/null
```

Choose a new filename for every backup and check command exit status. Listing the archive checks its table of contents; a restore into a separate database verifies the saved data. Backups contain source URLs, articles, and working reader tokens. Keep a protected off-host copy and retain `.env` and deployment configuration separately.

Restore into a new, unused database while keeping the original:

```sh
docker compose -f compose.yaml -f compose.postgres.yaml exec -T postgres createdb \
  -U rss_workshop -O rss_workshop -T template0 rss_workshop_restored
docker compose -f compose.yaml -f compose.postgres.yaml exec -T postgres pg_restore \
  -U rss_workshop -d rss_workshop_restored --exit-on-error --single-transaction \
  --no-owner --no-privileges < backups/postgres-before-upgrade.dump
docker compose -f compose.yaml -f compose.postgres.yaml exec -T postgres psql \
  -U rss_workshop -d rss_workshop_restored -v ON_ERROR_STOP=1 \
  -c 'SELECT version FROM schema_version;' \
  -c 'SELECT count(*) AS saved_feeds FROM feeds;' \
  -c 'SELECT count(*) AS saved_items FROM items;'
```

After those commands succeed, change only the database name in `DATABASE_URL` to `rss_workshop_restored` and recreate the app:

```sh
docker compose -f compose.yaml -f compose.postgres.yaml up -d --no-deps --no-build --wait rss-workshop
docker compose -f compose.yaml -f compose.postgres.yaml exec -T rss-workshop /rss-workshop -healthcheck
```

Sign in and verify the feed library and sample RSS/Atom output before discarding any old backup or database. Reverting the URI to the original database provides rollback while it remains available. Restored reader tokens and schedules come from the backup.

For an existing PostgreSQL server, use the same dump/restore options with that server's client tools and an administrator-created empty target database. Supply credentials through a protected password file or your database provider's tooling; avoid putting passwords directly in command arguments. Follow the provider's backup and recovery instructions when it manages the database.
