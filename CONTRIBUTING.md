# Contributing

Keep changes focused and include enough information to reproduce a bug or understand the intended behavior. Use [GitHub issues](https://github.com/Ldogg123/rss-workshop/issues) to discuss substantial design changes. Contributions are provided under the project's [MIT license](LICENSE); preserve all third-party notices.

## Local development

Install the Go version in `go.mod`, Make, Python 3.9 or newer, and a C compiler for race tests. No Node or frontend package installation is required.

```sh
go mod download
make build                    # bin/rss-workshop
make check                    # checks formatting, race tests, vet, Python checks, native smoke
```

Run a local instance using the [source setup](docs/deployment.md#run-from-source). Native development reads environment variables directly; `.env` is loaded by Compose only. Use a separate data directory and port when another instance is already running. Compose normally downloads published images. To test checkout changes in Docker, use the explicit [local build overrides](docs/deployment.md#build-container-images-from-source); the browser and static builds have separate overrides.

Format Go changes with `gofmt`. Changes to the visual editor or Chromium integration also need `make browser-test`; Docker/Compose changes need `make compose-test docker-smoke`. `make check-containers` runs all container checks, including PostgreSQL. These checks require Docker and Compose 2.24.4 or newer. The Docker CLI must be accessible to the test process; pass `DOCKER='sudo docker'` to Make when needed. See [AGENTS.md](AGENTS.md) for the code map, behavior to preserve, and detailed validation guidance, and [browser testing](docs/browser.md) for Chromium checks and optional live-site trials.

The application embeds its templates and assets in the Go binary, so frontend edits require rebuilding the server. Native development uses the same database and extraction paths as Docker. Docker's `RSS_DATA_DIR` and `POSTGRES_DATA_DIR` select precreated host directories; the app's mount needs UID/GID 65532 ownership. Follow [deployment](docs/deployment.md) for preparation. Use private temporary paths for container checks, with a test password; never inherit an operator's storage paths. Keep `.env`, databases, backups, feed tokens, and private source pages out of commits, Docker contexts, and test artifacts.

SQLite is the default when `DATABASE_URL` is blank. For PostgreSQL storage changes, set `RSS_TEST_POSTGRES_URL` to a dedicated disposable database and run `make postgres-test postgres-smoke`. `make docker-postgres-smoke` exercises a fresh isolated Compose server, database outages/reconnection, and PostgreSQL backup/restore. Pass `DOCKER='sudo docker'` if required. Never use a production database for these checks. See [PostgreSQL setup](docs/postgresql.md).

Keep both the default browser runtime and the lightweight static runtime usable. Static fetching must work without local Chromium or FlareSolverr. Fetch modes should share extraction, sanitization, scheduling, and saved-feed behavior. New source fetching must retain bounded concurrency, cancellation, response limits, and clear network-policy behavior. Avoid changing stored publication dates or GUIDs on refresh.

Publishing is a separate, manual step. Follow [release preparation](docs/releases.md) after the proposed changes pass review.

CI runs once per pull-request update and again when code reaches `main`; feature-branch and tag pushes do not create duplicate runs. Documentation-only PRs skip CI for root Markdown files, guides directly under `docs/`, and screenshots. Check their links and commands locally. License notices and recipe examples still receive checks. Newer automatic runs cancel older runs for the same PR or branch. See [CI behavior](docs/releases.md#validation) for details and manual checks.
