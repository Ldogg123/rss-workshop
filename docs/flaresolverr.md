# FlareSolverr integration

RSS Workshop can fetch selected feeds through an existing FlareSolverr service. FlareSolverr opens the source in its own browser and returns HTML after attempting the site's browser challenge. Some sites still require an unsupported CAPTCHA: the upstream project currently says its CAPTCHA solver adapters do not work. A successful fetch is site-dependent, not a guarantee that every Cloudflare page can be read. See the [official FlareSolverr documentation](https://github.com/FlareSolverr/FlareSolverr#readme).

## Configuration

Add these settings to the app's `.env`, using an address reachable from the app container:

```dotenv
FLARESOLVERR_URL=http://flaresolverr:8191
FLARESOLVERR_TIMEOUT=60s
FLARESOLVERR_SLOTS=1
```

`FLARESOLVERR_URL` accepts the HTTP(S) base URL or the `/v1` endpoint. Blank disables this integration. Use your existing service's hostname or LAN address; `localhost` inside the RSS Workshop container refers to that container. The example hostname assumes both services share a Docker network.

`FLARESOLVERR_TIMEOUT` permits 5 seconds through 2 minutes and defaults to 60 seconds. It is independent of `FETCH_TIMEOUT`; jobs have an additional 10 seconds for transport and cleanup. `FLARESOLVERR_SLOTS` permits 1–4 concurrent requests and defaults to 1. These requests also occupy the app's existing shared refresh/preview slots. A low concurrency limit is useful because the browser work runs on the FlareSolverr machine.

Recreate the app container after changing its environment:

```sh
docker compose up -d --no-build --wait
```

Use the same Compose files as your original installation; add `sudo` if Docker access requires it. The default image includes local Chromium, while FlareSolverr runs separately. This integration also works with the static-only runtime selected by `-f compose.yaml -f compose.static.yaml`. With `RSS_IMAGE` blank, that override selects the matching published static image. RSS Workshop does not install, upgrade, or reconfigure the FlareSolverr service.

## Using it

1. Create or edit a feed and choose **Fetch mode → FlareSolverr (Cloudflare)**.
2. Choose **Choose elements visually** to load that page through FlareSolverr, or enter CSS/XPath selectors directly.
3. Check **Preview items**, then save the feed. Scheduled refreshes use the same fetch mode.

The visual workspace also offers **Page version → FlareSolverr (Cloudflare)**. Reload after changing the page version. **Done** keeps the successfully loaded mode. Existing **Auto** recipes retain Auto unless you explicitly choose another page version.

This is an explicit per-feed choice. **Auto** continues to try static HTML, then local Chromium only when a successful static fetch yields no items; HTTP errors and access blocks do not invoke FlareSolverr. Local Chromium's wait selector and settling delay do not apply to FlareSolverr and are hidden in this mode.

The integration makes stateless `request.get` calls. It does not retain sessions or reuse returned cookies in later requests. Returned HTML uses the existing extraction, sanitization, preview, retention, and RSS/Atom output paths. Recipe export/import preserves the fetch mode; the server endpoint stays in deployment configuration and is not exported with recipes.

## Network boundary and troubleshooting

The endpoint is a trusted administrator-configured service, separate from a feed's source URL. A private FlareSolverr endpoint does not need an `ALLOW_CIDRS` exception. Keep that service reachable only by trusted clients, consistent with [upstream deployment guidance](https://github.com/FlareSolverr/FlareSolverr#docker).

RSS Workshop checks the initial source URL and the final URL returned by FlareSolverr against its source destination policy. It cannot enforce its local Chromium proxy policy inside the remote browser. Intermediate redirects, subresources, DNS resolution, and actual network connections are controlled by the FlareSolverr deployment. Apply network restrictions there when the browser must be prevented from reaching internal services; app-side URL checks do not provide that remote isolation.

The dashboard reports configured capacity and current requests, not a remote health check. `/readyz` checks the app's selected database, without checking FlareSolverr. An unavailable solver does not make static feeds unavailable. A failed or empty extraction keeps the last successful feed output and follows the usual refresh backoff.

For an unavailable-service error, check that the endpoint is reachable from the app container and that its `/v1` API is running. A solver timeout or unsupported CAPTCHA needs investigation on the FlareSolverr side; increasing the timeout helps only when the challenge can be solved. Cancelling a preview stops the app's request, but the remote browser may continue until its own timeout. The app does not offer interactive CAPTCHA completion or account login sessions.
