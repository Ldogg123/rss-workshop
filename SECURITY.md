# Security reports

For a suspected vulnerability, use [**Security → Report a vulnerability**](https://github.com/Ldogg123/rss-workshop/security/advisories/new) to contact the maintainers privately. Do not put working exploit details, admin passwords, feed bearer tokens, or private source URLs in public issues.

This project is a single-administrator, self-hosted service. Keep administration behind HTTPS or a trusted local connection. RSS and Atom links contain bearer tokens: anyone with a link can read that feed. Rotate links if they are disclosed.

Optional [health notifications](docs/operations.md#notifications) add one outbound channel: messages go to `NOTIFY_URL`, an address the administrator trusts like `FLARESOLVERR_URL`, so it may be private and `ALLOW_CIDRS` does not apply; environment proxies and redirects are not followed. `NOTIFY_URL` and `NOTIFY_TOKEN` are secrets. A topic on a public ntfy server works as a password, and webhook URLs often embed a token; neither value appears in logs, errors, metrics or the web interface, but `docker compose config` prints them. Messages carry feed titles, failure counts and redacted errors, never source URLs, reader links or story text. Titles can still reveal what you follow, so prefer a notification service you host yourself.

Database connection URIs contain credentials and belong in private deployment configuration. For an existing shared PostgreSQL server, use a dedicated database and non-superuser role with ownership of that database, and configure verified TLS. Database connections are trusted administrator configuration and are separate from source-fetching `ALLOW_CIDRS` rules. Run one app process per database.

Security fixes will target the latest released version. No long-term maintenance window is promised. Before publishing a release, run the documented checks and review changes in Go, Chromium, and the base images. A clean vulnerability scan is a point-in-time check, not a guarantee about unknown vulnerabilities.

See [deployment](docs/deployment.md), [browser isolation](docs/browser.md), and [FlareSolverr's remote network boundary](docs/flaresolverr.md) for the supported operating model and its limits.
