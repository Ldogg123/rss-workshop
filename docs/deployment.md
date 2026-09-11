# Deployment and configuration

Run one RSS Workshop process per database. The default Compose installation includes sandboxed Chromium and SQLite on local disk; an explicit `DATABASE_URL` selects PostgreSQL. Docker deployments require Docker Engine and Compose 2.24.4 or newer. The app runs as UID/GID 65532 with a read-only root filesystem; a configurable host directory supplies persistent storage at `/data`.

## Docker Compose

For a new installation, create a private `.env` file from [.env.example](../.env.example), set `ADMIN_PASSWORD` or `ADMIN_PASSWORD_HASH`, then prepare storage and start the app with Chromium:

```sh
git clone https://github.com/Ldogg123/rss-workshop.git
cd rss-workshop
cp .env.example .env
chmod 600 .env
# Edit .env before starting.
sudo mkdir -p -m 700 ./data
sudo chown 65532:65532 ./data
sudo chmod 700 ./data
docker compose up -d --pull always --wait
```

Use `sudo docker` if your account requires it. Compose defaults to project and service name `rss-workshop` and container `rss-workshop-rss-workshop-1`. It downloads `ghcr.io/ldogg123/rss-workshop:latest`, the latest stable browser image; no local build is required. Leave `RSS_IMAGE` blank to use that default, or [select a tag or digest](#registry-images). Keep the same Compose files and order for subsequent commands.

`RSS_DATA_DIR` selects the host directory mounted at `/data`, defaulting to `./data`. Set it in `.env` to change the location, then use that same path in the directory preparation commands. Relative paths resolve from the directory containing `compose.yaml`; an absolute path such as `/srv/rss-workshop/data` is useful when managing storage separately from the checkout. The directory must exist and be writable by UID/GID 65532 before startup. Compose refuses a missing directory instead of silently creating it as root. The container's `DATA_DIR=/data` remains fixed; `RSS_DATA_DIR` is a host-side Compose setting.

The directory commands assign numeric UID/GID 65532 without creating a host user or group. They avoid `install -o 65532 -g 65532`, which some implementations reject with an `invalid user` error when those IDs have no matching host account. Keep mode `700` on the app directory.

Keep the same host directory on later runs. If upgrading from an existing SQLite named-volume deployment, follow [storage migration and recovery](operations.md#move-an-existing-sqlite-volume-to-a-host-directory) before recreating the app. Changing a mount to an empty directory starts a separate, empty SQLite library; it does not copy the old database. Keep the original data until migration is verified. PostgreSQL storage migration is covered in its [setup guide](postgresql.md#existing-postgresql-named-volumes).

The default host directories are ignored by Git and Docker builds. Keep custom data directories outside the checkout, or explicitly exclude them from both Git and the Docker build context.

Keep the default Chromium sandbox and resource settings. See [Chromium rendering](browser.md) and [FlareSolverr](flaresolverr.md) for their configuration and network boundaries.

## Lightweight static runtime

If you only need static pages or an external FlareSolverr service, select the static override. With `RSS_IMAGE` blank, it downloads `ghcr.io/ldogg123/rss-workshop:latest-static` automatically. This image contains the Go application and CA certificates, with no shell or local browser:

```sh
docker compose -f compose.yaml -f compose.static.yaml up -d --pull always --wait
```

Keep the static override for subsequent operations. If `RSS_IMAGE` already has an explicit value, change it to a matching static image or clear it to use the default. Data paths and database selection work the same way in both runtimes.

## Gluetun VPN

The optional [Gluetun override](../compose.gluetun.yaml) connects RSS Workshop to an existing Gluetun container. Follow the [Gluetun setup guide](gluetun.md) to publish the UI port on Gluetun, choose an unused internal port, and configure access to PostgreSQL or FlareSolverr. It works with both runtimes; an HTTP proxy environment variable does not route the app's guarded fetcher.

## Database selection

With `DATABASE_URL` blank or unset, the app uses `DATA_DIR/rss.db`. A `postgres://` or `postgresql://` URI selects an existing PostgreSQL database. See [PostgreSQL setup](postgresql.md) for credentials, TLS, and the optional `compose.postgres.yaml` server. The PostgreSQL override works with both runtimes.

Changing the selected database does not copy recipes, articles, or reader tokens. A fresh database starts empty; the old SQLite file or PostgreSQL database remains separate. Keep one app process per database and use the matching [backup procedure](operations.md#backup-and-restore).

## Registry images

The default Compose files follow stable releases. These moving tags point to the corresponding versioned images:

| Tag | Runtime |
| --- | --- |
| `ghcr.io/ldogg123/rss-workshop:latest` | Browser default |
| `ghcr.io/ldogg123/rss-workshop:latest-browser` | Explicit alias for the same browser image |
| `ghcr.io/ldogg123/rss-workshop:latest-static` | Static default |

To stay on a particular release, set the optional `RSS_IMAGE` in `.env` to its versioned tag or immutable digest. The published `v0.2.0-browser` and `v0.2.0-static` tags remain available; for example:

```dotenv
RSS_IMAGE=ghcr.io/ldogg123/rss-workshop:v0.2.0-browser
```

Recreate the app with the same runtime and other overrides used by your installation:

```sh
docker compose up -d --pull always --wait
```

Operator files set [`pull_policy: always`](https://docs.docker.com/reference/compose-file/services/#pull_policy), so Compose checks the registry when starting the app. A moving tag does not automatically replace a running container: run the command above to pull and recreate it when the image changes. `docker compose restart` alone keeps the existing image. See Docker's [`compose up` behavior](https://docs.docker.com/reference/cli/docker/compose/up/).

An explicit image must match the runtime: use a browser image with the base file, or a static image with `-f compose.yaml -f compose.static.yaml`. Existing `.env` values remain in effect when updating the checkout. Prefer immutable digests and keep the previous digest and data backup for rollback; [release preparation](releases.md) describes the published variants.

For compatibility, existing commands may retain `compose.browser.yaml` or a final `compose.image.yaml`. The browser override selects the browser runtime; the image override requires an explicit `RSS_IMAGE` and removes any build configuration. Neither is needed for new installations.

## Build container images from source

To run changes from your local checkout, prepare `.env` and host storage as above, then add the development build override:

```sh
docker compose -f compose.yaml -f compose.build.yaml up -d --build --wait
```

This builds the Chromium runtime from `Dockerfile.browser` as `rss-workshop:local`. For the lightweight runtime, use its matching runtime and build overrides:

```sh
docker compose -f compose.yaml -f compose.static.yaml -f compose.build.static.yaml up -d --build --wait
```

This builds `Dockerfile` as `rss-workshop:local-static`. Both build overrides select local image names independently of `RSS_IMAGE` and replace the operator pull policy with `pull_policy: build`. Include any database or network overrides before the build override, and keep the same files for subsequent operations. Use a separate project, port, and data directory when testing alongside an existing installation.

## Public URL and HTTPS

The default binding is `127.0.0.1:8080:8080`, with `PUBLIC_BASE_URL=http://localhost:8080`. For remote access, put the app behind a TLS reverse proxy and set the exact public origin, such as `https://rss.example.net`. Path-prefix hosting is unsupported. Feed URLs and origin checks use this configured address; forwarded headers do not replace it.

A host-installed Caddy proxy can use:

```caddyfile
rss.example.net {
    reverse_proxy 127.0.0.1:8080 {
        transport http {
            response_header_timeout 150s
        }
    }
}
```

Supply your hostname and DNS/TLS configuration. The [Caddy reverse proxy directive](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy) forwards to the host's loopback port; a containerized proxy needs a shared network instead. Preserve the browser's `Origin` header, avoid caching management/API responses, and allow at least 150 seconds for the maximum FlareSolverr timeout and cleanup.

For direct LAN access, deliberately bind the app to a LAN address and set `PUBLIC_BASE_URL` to the same origin. Use HTTPS across untrusted networks. HTTPS enables secure session cookies. Redact or disable proxy access logs for `/feeds/`, whose paths contain bearer tokens.

## Environment variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `ADMIN_PASSWORD` | required unless hash supplied | Your chosen admin password; no enforced length policy |
| `ADMIN_PASSWORD_HASH` | empty | Standard bcrypt hash; takes precedence and retains bcrypt's input limit. Use `ADMIN_PASSWORD` for long passphrases. |
| `PUBLIC_BASE_URL` | `http://localhost:8080` | Stable public origin without a path |
| `LISTEN_ADDR` | `:8080` | Native bind address |
| `DATA_DIR` | `./data`, `/data` in Docker | Local SQLite directory |
| `RSS_DATA_DIR` | `./data` | Compose host directory mounted at `/data`; precreate with UID/GID 65532 ownership |
| `DATABASE_URL` | empty | PostgreSQL connection URI; blank selects SQLite |
| `POSTGRES_PASSWORD` | required for PostgreSQL override | Bootstrap password for the optional dedicated server |
| `POSTGRES_DATA_DIR` | `./postgres-data` | Compose host directory for the optional PostgreSQL server; precreate before startup |
| `STATIC_WORKERS` | `16` | 1–64 shared refresh/preview slots |
| `FETCH_TIMEOUT` | `30s` | 1s–2m static/local Chromium deadline |
| `CHROMIUM_PATH` | empty | Native browser executable; browser image sets `/usr/bin/chromium` |
| `BROWSER_SLOTS` | `2` | 1–8 local browser jobs |
| `FLARESOLVERR_URL` | empty | Trusted service base URL or `/v1` endpoint |
| `FLARESOLVERR_TIMEOUT` | `60s` | 5s–2m solver deadline, independent of `FETCH_TIMEOUT` |
| `FLARESOLVERR_SLOTS` | `1` | 1–4 solver jobs |
| `MAX_ITEMS` | `500` | 1–10,000 retained items per feed |
| `ALLOW_CIDRS` | empty | Explicit comma-separated internal-source exceptions |
| `RSS_IMAGE` | empty; `latest` browser or `latest-static` | Optional moving tag, version pin or digest; must match the selected runtime |
| `GLUETUN_CONTAINER` | required for Gluetun override | Existing, running Gluetun container name on this Docker host |
| `GLUETUN_APP_PORT` | `8080` | App's internal listening port when sharing Gluetun; publish it on Gluetun |

Keep `.env` out of version control. Single-quote bcrypt hashes and literal secrets in `.env` so Compose preserves dollar signs. Percent-encode reserved characters inside connection-URI passwords. Recreate the container after environment changes; restarting the process invalidates login sessions.

Leave `ALLOW_CIDRS` empty for public sources. Static fetching and the local Chromium proxy check destinations before dialing; environment HTTP proxies are ignored. FlareSolverr has a separate remote network boundary, and its private service endpoint does not require a source allowlist exception.

## Run a prebuilt executable

Download the archive for your Linux CPU from [Releases](https://github.com/Ldogg123/rss-workshop/releases): `amd64` for x86-64, or `arm64` for 64-bit ARM. These are the supported prebuilt platforms. Each archive includes the executable, application license, dependency notices, and build information. Go, Docker, a separate SQLite installation, and a frontend build are not required. The host must have a working system CA certificate store for HTTPS.

For example, download and verify `v0.2.0` for Linux x86-64 in an empty directory:

```sh
curl -fLO https://github.com/Ldogg123/rss-workshop/releases/download/v0.2.0/rss-workshop-v0.2.0-linux-amd64.tar.gz
curl -fLO https://github.com/Ldogg123/rss-workshop/releases/download/v0.2.0/rss-workshop-v0.2.0-linux-amd64.tar.gz.sha256
sha256sum -c rss-workshop-v0.2.0-linux-amd64.tar.gz.sha256
tar -xzf rss-workshop-v0.2.0-linux-amd64.tar.gz
cd rss-workshop-v0.2.0-linux-amd64
./rss-workshop -version
```

Use `arm64` in both filenames for ARM. Only extract after the checksum succeeds. The `debian-sources-*` release assets are dependency sources for container redistribution; they are not needed to install the native executable.

### Configure and run

Run as your normal user or a dedicated non-root service account. Native execution reads environment variables and **does not load `.env` automatically**. For a local SQLite instance in Bash, choose an admin password:

```bash
read -r -s -p 'Admin password: ' ADMIN_PASSWORD
printf '\n'
export ADMIN_PASSWORD
export PUBLIC_BASE_URL=http://localhost:8080
export LISTEN_ADDR=127.0.0.1:8080
export DATA_DIR="$HOME/.local/share/rss-workshop"
export DATABASE_URL=
install -d -m 700 "$DATA_DIR"
./rss-workshop
```

Open [localhost:8080](http://localhost:8080). SQLite is stored at `$DATA_DIR/rss.db`; keeping it outside the extracted archive preserves the library when replacing the executable. Use another port and data directory if an instance is already running. For an unattended deployment, supply these variables through your service manager and keep the same absolute data path across restarts. For LAN access, set `LISTEN_ADDR` to an appropriate listening address and `PUBLIC_BASE_URL` to the origin readers and your browser will use.

Static fetching and visual selection work without Chromium. To render JavaScript pages, install Chromium separately and export `CHROMIUM_PATH` with its executable's absolute path before starting the app, for example `export CHROMIUM_PATH=/usr/bin/chromium`. The host must support Chromium's sandbox; run as non-root and follow [browser rendering](browser.md). [External FlareSolverr](flaresolverr.md) also works without local Chromium. To use an existing PostgreSQL server, set `DATABASE_URL` as described in [PostgreSQL setup](postgresql.md); changing the database does not migrate existing data.

For upgrades, back up the database, stop the old process, unpack the new executable, and restart it with the same environment and data path. Direct upgrades from every released database schema are supported; see [upgrade compatibility](operations.md#upgrades). Keep one application process per database. Preserve the bundled `LICENSE` and `licenses/` notices when redistributing the executable.

## Run from source

Use the Go version specified in [go.mod](../go.mod) and Make; tests also require Python 3.11+ and a C compiler. `make build` produces `bin/rss-workshop`. Follow [native configuration](#configure-and-run), then run `./bin/rss-workshop` from the repository root instead of `./rss-workshop`. See [contributing](../CONTRIBUTING.md) for build and test commands.

Check readiness, sign in through the final URL, and preview a source before connecting readers. Follow [operations](operations.md) for health checks, verified backups, restores, and upgrades.
