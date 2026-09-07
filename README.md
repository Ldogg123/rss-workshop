# RSS Workshop

Turn website pages into persistent RSS and Atom feeds. Choose repeating elements visually, refine CSS or XPath selectors with live highlights, and let scheduled refreshes collect new stories.

The default Docker installation includes Chromium for JavaScript pages, SQLite, a single-admin interface, and dark mode. Data stays in local folders on your host.

## Quickstart

For a new installation with Docker Engine and Compose installed:

```sh
git clone https://github.com/Ldogg123/rss-workshop.git
cd rss-workshop
cp .env.example .env
chmod 600 .env
# Set ADMIN_PASSWORD in .env to a unique password of 12–72 bytes.
sudo install -d -m 700 -o 65532 -g 65532 ./data
docker compose -f compose.yaml -f compose.image.yaml up -d --pull always --wait
```

Open [localhost:8080](http://localhost:8080) and sign in. Use `sudo docker` if your account requires it. App settings are in `.env`; SQLite data is in `./data/rss.db`. `RSS_IMAGE` selects the published browser image, so startup downloads it without compiling. Use the same Compose files for subsequent commands.

For LAN access, HTTPS, custom paths, or the smaller static runtime, see [deployment](docs/deployment.md). Check [Releases](https://github.com/Ldogg123/rss-workshop/releases) for versions and [registry deployment](docs/deployment.md#registry-images) for image selection or building locally. Existing named-volume installations should follow the [migration guide](docs/operations.md#move-an-existing-sqlite-volume-to-a-host-directory) first.

## Run without Docker

Download a Linux `amd64` (x86-64) or `arm64` executable archive and its `.sha256` file from [Releases](https://github.com/Ldogg123/rss-workshop/releases). The executable includes the web UI and SQLite support; Go and Docker are not required. Chromium is optional and installed separately on the host.

Follow [native installation](docs/deployment.md#run-a-prebuilt-executable) to verify the download, set the password and data path, and start the server.

## Optional PostgreSQL

For a fresh PostgreSQL installation, use the preparation above but replace its final startup command with the one below. Generate a password with `python3 -c 'import secrets; print(secrets.token_hex(24))'`, then put the same generated value in both `.env` entries:

```dotenv
POSTGRES_PASSWORD='YOUR_GENERATED_HEX_PASSWORD'
DATABASE_URL='postgres://rss_workshop:YOUR_GENERATED_HEX_PASSWORD@postgres:5432/rss_workshop?sslmode=disable'
```

```sh
sudo install -d -m 755 ./postgres-data
docker compose -f compose.yaml -f compose.postgres.yaml -f compose.image.yaml up -d --pull always --wait
```

This includes Chromium and stores PostgreSQL in `./postgres-data`. Both data paths can be changed in `.env`. Switching databases does not migrate existing data; see [PostgreSQL setup](docs/postgresql.md) for existing servers and migration.

## Create a feed

1. Choose **New feed** and enter a name and page URL.
2. Choose **Choose elements visually**. Select a repeating card, then its title, link, description, image, and date. Edit CSS or XPath beside the live preview to fine-tune the matches.
3. Choose **Preview items**, check the results, and save.
4. Copy the RSS or Atom URL into your reader.

Reader requests use saved items; they never fetch the source. Failed refreshes preserve the last successful output. Anyone with a feed link can read it; **Reset feed links** revokes its existing RSS and Atom URLs.

## Documentation

- [Visual selectors](docs/visual-selector.md), [dates](docs/dates.md), and [RSS/Atom output](docs/atom.md)
- [Browser rendering](docs/browser.md), [FlareSolverr](docs/flaresolverr.md), and [Gluetun VPN](docs/gluetun.md)
- [Recipe import/export](docs/recipe-portability.md) and [example recipes](docs/examples/all-visual-recipes.json)
- [Backups, restores, and upgrades](docs/operations.md)
- [Development](CONTRIBUTING.md), [repository guide](AGENTS.md), [releases](docs/releases.md), and [changelog](CHANGELOG.md)

## License and security

RSS Workshop is [MIT licensed](LICENSE). Dependencies retain their own licenses; see [licensing and redistribution](docs/licensing.md) and [dependency notices](docs/dependencies.md). Report vulnerabilities using the process in [SECURITY.md](SECURITY.md).
