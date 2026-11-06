# FlareSolverr integration

RSS Workshop can fetch selected feeds through an existing FlareSolverr service. FlareSolverr opens the source in its own browser and returns HTML after attempting the site's browser challenge. Some sites still require an unsupported CAPTCHA: the upstream project currently says its CAPTCHA solver adapters do not work. A successful fetch is site-dependent, not a guarantee that every Cloudflare page can be read. See the [official FlareSolverr documentation](https://github.com/FlareSolverr/FlareSolverr#readme).

## Configuration

FlareSolverr is configured on the server, in the environment RSS Workshop starts with:

```dotenv
FLARESOLVERR_URL=http://flaresolverr:8191
FLARESOLVERR_TIMEOUT=60s
FLARESOLVERR_SLOTS=1
```

With Docker Compose, put these lines in the app's `.env`. A [native executable](deployment.md#run-a-prebuilt-executable) does not read `.env`: export them in the shell or service definition that starts it.

`FLARESOLVERR_URL` accepts the HTTP(S) base URL or the `/v1` endpoint. Blank disables this integration. Use an address that RSS Workshop itself can reach: your existing service's hostname or LAN address. The example hostname assumes both containers share a Docker network. `localhost` or `127.0.0.1` inside the RSS Workshop container refers to that container, with one exception: when RSS Workshop and FlareSolverr both [share Gluetun's network](gluetun.md#postgresql-and-flaresolverr), `http://127.0.0.1:8191` is the correct address. A native executable on the host that runs FlareSolverr can likewise use `127.0.0.1` with the port FlareSolverr publishes.

`FLARESOLVERR_TIMEOUT` permits 5 seconds through 2 minutes and defaults to 60 seconds. It is independent of `FETCH_TIMEOUT`; jobs have an additional 10 seconds for transport and cleanup. `FLARESOLVERR_SLOTS` permits 1–4 concurrent requests and defaults to 1. These requests also occupy the app's existing shared refresh/preview slots. A low concurrency limit is useful because the browser work runs on the FlareSolverr machine; see [capacity](#capacity).

The settings are read once at startup. After changing them, recreate the app container, keeping the already-local image for this configuration change:

```sh
docker compose up -d --no-build --pull never --wait
```

Use the same `-f` Compose files as your original installation, in the same order; add `sudo` if Docker access requires it. `docker compose restart` is not enough, because it restarts the existing container with the environment it was created with. For a native executable, stop it and start it again with the new values exported. The default image includes local Chromium, while FlareSolverr runs separately. This integration also works with the static-only runtime selected by `-f compose.yaml -f compose.static.yaml`. With `RSS_IMAGE` blank, that override selects the matching published static image. RSS Workshop does not install, upgrade, or reconfigure the FlareSolverr service.

## Using it

1. Create or edit a feed and choose **Fetch mode → FlareSolverr (Cloudflare)**.
2. Choose **Choose elements visually** to load that page through FlareSolverr, or enter CSS/XPath selectors directly.
3. Check **Preview items**, then save the feed. Scheduled refreshes use the same fetch mode.

The visual workspace also offers **Page version → FlareSolverr (Cloudflare)**. Reload after changing the page version. **Done** keeps the successfully loaded mode. Existing **Auto** recipes retain Auto unless you explicitly choose another page version.

This is an explicit per-feed choice. **Auto** continues to try static HTML, then local Chromium only when a successful static fetch yields no items; HTTP errors and access blocks do not invoke FlareSolverr. Local Chromium's wait selector and settling delay do not apply to FlareSolverr and are hidden in this mode.

The integration makes stateless `request.get` calls. It does not retain sessions or reuse returned cookies in later requests. Returned HTML uses the existing extraction, sanitization, preview, retention, and RSS/Atom output paths. Recipe export/import preserves the fetch mode; the server endpoint stays in deployment configuration and is not exported with recipes.

## Capacity

RSS Workshop starts a FlareSolverr job only when a slot is free, and each job uses one slot at a time. A refresh needs FlareSolverr for its list page and, if it renders its [article pages](atom.md#full-article-content) there, for those as well, one by one. Article pages fetched as plain HTML, the default, do not use FlareSolverr: once the list page is solved, the slot goes to the next job while the refresh fetches them.

When more FlareSolverr feeds are due than there are slots, as after a restart, an outage or an import, the rest stay due and start oldest first as slots free up. Waiting for a slot is not a failure: it records no error and does not back a feed off. Feeds in other fetch modes are not held back.

**Preview items** and **Choose elements visually** do not wait either. When every slot is in use, they report `FlareSolverr is busy` straight away. They have no claim on the next slot to free up: while FlareSolverr feeds are due, the scheduler gives each freed slot to the oldest of them within about a second. With nothing queued, a preview can run as soon as the running refresh has finished; after a restart, an outage or an import, it may stay busy until the due FlareSolverr feeds have refreshed. Raising `FLARESOLVERR_SLOTS` helps only if the FlareSolverr machine can run that many browsers at once.

FlareSolverr cannot cancel a request. When one is interrupted after reaching the service, for example by closing the visual workspace during a load, its slot stays in use until the remote browser's own deadline has passed. A request that never connected frees its slot at once. The dashboard and the `rss_workshop_flaresolverr_active` metric count both kinds of busy slot.

## Network boundary

The endpoint is a trusted administrator-configured service, separate from a feed's source URL. A private FlareSolverr endpoint does not need an `ALLOW_CIDRS` exception. Keep that service reachable only by trusted clients, consistent with [upstream deployment guidance](https://github.com/FlareSolverr/FlareSolverr#docker).

RSS Workshop checks the initial source URL and the final URL returned by FlareSolverr against its source destination policy. It cannot enforce its local Chromium proxy policy inside the remote browser. Intermediate redirects, subresources, DNS resolution, and actual network connections are controlled by the FlareSolverr deployment. Apply network restrictions there when the browser must be prevented from reaching internal services; app-side URL checks do not provide that remote isolation.

## Troubleshooting

The dashboard reports configured capacity and current requests, not a remote health check. `/readyz` checks the app's selected database, without checking FlareSolverr. An unavailable solver does not make static feeds unavailable. A failed or empty extraction keeps the last successful feed output and follows the usual refresh backoff.

To test the service from the app, open **Choose elements visually** with **Page version → FlareSolverr (Cloudflare)**, or run **Preview items** in this mode. A failure there, on the feed card and in [Diagnostics](operations.md#feed-diagnostics) names the step that failed. These messages never include the service address, the source URL or text from the reply, so check `FLARESOLVERR_URL` where you set it.

| Message begins with | What to check |
| --- | --- |
| `FlareSolverr is not configured on this server` | `FLARESOLVERR_URL` is blank in the environment the server started with. Set it as described under [configuration](#configuration), then recreate the container or start the native executable again; `docker compose restart` keeps the old environment. |
| `FlareSolverr DNS lookup failed` | The host name does not resolve where RSS Workshop runs. A Compose service name resolves only for containers on the same Docker network; otherwise use a LAN address or a name your DNS knows. |
| `FlareSolverr connection refused` | The host answered but nothing listens on that port: FlareSolverr is stopped, still starting, or on another port (8191 by default). Inside a container, `localhost` is RSS Workshop's own container unless both share Gluetun's network. |
| `FlareSolverr connection timed out` | Nothing answered at that address: a wrong IP address, a host that is down, or a firewall dropping the connection, such as Gluetun's outbound firewall without a `FIREWALL_OUTBOUND_SUBNETS` exception. |
| `FlareSolverr connection failed: there is no network route` | The address is on a network this host or container cannot reach. |
| `FlareSolverr connection failed before a request was sent` | Usually TLS: an `https://` address for a service that speaks plain HTTP, or a certificate the server does not trust. |
| `FlareSolverr address returned HTTP 404` | A web server answers, but not FlareSolverr's API. Use the base URL, such as `http://flaresolverr:8191`, or its `/v1` endpoint; a reverse proxy must pass `/v1` through. |
| `FlareSolverr address replied, but not as FlareSolverr` | Another service or a proxy answers at that address; the HTTP status in parentheses hints at which. `401` or `403` suggests an authenticating proxy, and `502` or `503` a proxy whose upstream is down. |
| `FlareSolverr closed the connection without a complete reply` | FlareSolverr, or a proxy in front of it, dropped the request, typically while crashing or restarting. Check its logs. |
| `FlareSolverr accepted the request but did not reply before the deadline` | The request reached FlareSolverr, which did not answer in time: at most `FLARESOLVERR_TIMEOUT` and a short grace period, less for an article page late in a refresh. It may be overloaded or busy with another client's requests. |
| `FlareSolverr is busy` | Every slot is in use by a refresh or another preview. Due FlareSolverr feeds take freed slots first, so after an import this can last until they have refreshed. See [capacity](#capacity). |
| `timed out waiting for a free FlareSolverr slot` | A slot stayed in use for the whole deadline, usually after an interrupted request. `no time was left to send the FlareSolverr request` is the same deadline running out just as a slot freed. |

Messages that FlareSolverr reports itself, such as an unsolved CAPTCHA, a timeout or a failed browser request, need investigation on the FlareSolverr side; increasing the timeout helps only when the challenge can be solved. Cancelling a preview stops the app's request, but the remote browser may continue until its own timeout. The app does not offer interactive CAPTCHA completion or account login sessions.
