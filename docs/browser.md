# Chromium rendering

The default Docker deployment includes Chromium for JavaScript rendering. It reuses one managed Chromium process and creates a new incognito context for each render. Cookies and local storage are discarded afterward. Rendered pages use the same extraction and sanitization as static HTML.

## Deploy

```sh
# Configure .env and prepare the host data directory first; see deployment.
docker compose up -d --pull always --wait
```

This pulls `ghcr.io/ldogg123/rss-workshop:v0.1.1-browser` when `RSS_IMAGE` is blank or unset. An explicit `RSS_IMAGE` must select a browser image for this setup. See [deployment](deployment.md) for credentials and public URLs. Use `sudo docker` if your account requires it.

For a local source build, use the explicit [container build overrides](deployment.md#build-container-images-from-source). The [browser Dockerfile](../Dockerfile.browser) pins the Debian base digest and Chromium package version; maintainers should review those pins for security updates before rebuilding. `CHROMIUM_VERSION` is a build argument for upgrades.

For a smaller runtime without local Chromium, use the [static override](../compose.static.yaml):

```sh
docker compose -f compose.yaml -f compose.static.yaml up -d --pull always --wait
```

With `RSS_IMAGE` blank or unset, this pulls `ghcr.io/ldogg123/rss-workshop:v0.1.1-static`. If you set `RSS_IMAGE` explicitly, select a static image and keep this override on later runs. This keeps the same host data directory and supports static HTTP fetching and external FlareSolverr. It clears Chromium configuration and replaces the browser's sandbox and resource settings with the static runtime's settings. Saved Chromium recipes need the browser runtime to refresh successfully.

For native installations, set `CHROMIUM_PATH` to an installed Chromium executable and run the app as a non-root user on a host that supports its sandbox. Docker deployments do not require host Chromium or Node.

## Recipe settings

- **Static HTTP:** fetch and extract HTML without JavaScript.
- **Chromium:** always render, then extract the resulting DOM.
- **Auto:** try static first. Render once only when a successful HTTP response yields zero valid items. HTTP failures, access blocks, and invalid selectors do not trigger browser retries.
- **FlareSolverr (Cloudflare):** use a separately configured external browser service for challenge pages. This mode does not require local Chromium and is never an Auto fallback. Some CAPTCHAs remain unsupported. See [FlareSolverr setup and network limits](flaresolverr.md).
- **Wait for element:** an optional CSS selector that must exist in the local Chromium page before extraction. This is CSS even for XPath extraction recipes; it does not apply to FlareSolverr.
- **Extra render wait:** an optional 0–5000 ms settling delay after local Chromium navigation/readiness. Prefer a specific readiness selector for pages that load asynchronously. This delay does not apply to FlareSolverr.

Changing fetch settings invalidates source validators and in-flight results. Browser-derived results do not retain static HTTP validators, so Auto cannot mistake a source 304 for proof that rendered content is unchanged.

## Bounds and recovery

`BROWSER_SLOTS` defaults to 2 and permits 1–8 concurrent browser jobs. Previews and scheduled renders share this pool as well as the overall `STATIC_WORKERS` job admission limit. Waiting renders occupy bounded job slots; there is no unbounded browser queue. The authenticated dashboard reports browser capacity and current activity. One additional blank control tab belongs to the managed process.

The existing `FETCH_TIMEOUT` bounds each refresh/preview, including readiness and settling. Browser initialization has its own 15-second watchdog. Tabs and contexts are disposed on success, error, cancellation, and timeout. A crashed process fails its active jobs and is recreated for later jobs. The process recycles after 100 completed renders when no renders are active. Downloads are denied and popup targets are closed.

Rendered snapshots are limited to 4 MiB before the regular extraction limits apply. The proxy caps active connections at 64, ordinary HTTP response streams at 16 MiB, and each direction of CONNECT tunnels at 32 MiB with a two-minute lifetime. These limits may reject resource-heavy sites. The browser container limits memory to 2 GiB and process count to 256, with 256 MiB shared memory and 256 MiB temporary storage.

## Sandbox and outbound policy

The image runs as UID/GID 65532 with no Linux capabilities, a read-only root filesystem, and `no-new-privileges`. Chromium's sandbox stays enabled; the application refuses to launch it as root. The seccomp profile starts from Docker/Moby's standard profile and additionally allows `clone`, `unshare`, `setns`, and `chroot`, enabling Chromium to establish its own nested user/PID/network namespaces and chroot. All other profile restrictions remain. No privileged mode, host-wide AppArmor relaxation, or `--no-sandbox` flag is used.

The [seccomp profile](../deploy/chromium-seccomp.json) derives from [Moby's default profile](https://github.com/moby/profiles/blob/main/seccomp/default.json), with its [license preserved](licenses/Moby-profiles-LICENSE). Integration tests assert PID namespaces, network namespaces, and Seccomp-BPF. Hosts that forbid unprivileged user namespaces need a sandbox-compatible configuration; the app does not silently disable sandboxing.

Chromium uses a loopback-only HTTP proxy in the Go process. The proxy resolves and validates every actual TCP destination using the same IP policy and allowlist as static fetching, then dials the checked IP directly. That covers navigations, redirects, HTTP subresources, and HTTPS CONNECT destinations without a second DNS lookup. Implicit loopback proxy bypass is removed. CONNECT is restricted to port 443; HTTPS on other ports is currently unsupported. QUIC is disabled, non-proxied WebRTC UDP is disabled, and ordinary WebSocket URLs are blocked through DevTools. Source credentials and bearer feed tokens are not logged by the proxy.

This is application-level outbound enforcement, not a host packet firewall. HTTPS payloads stay encrypted end to end and are not inspected by the proxy. Chromium's broker process still has the container's network access; an engine exploit or a protocol path that ignores browser proxy settings is outside this enforcement guarantee. No claim is made that these flags prevent every possible non-HTTP network path. Use a dedicated host/container network boundary if the broker itself must be unable to reach internal networks. ALLOW_CIDRS is an explicit administrator exception and should be narrowly scoped. Tests cover private HTTP subresources and redirects, plus direct proxy CONNECT policy; they do not prove universal network isolation.

## Validation

```sh
make browser-test
make docker-smoke
```

Use `DOCKER='sudo docker'` with Make when needed; the smoke target forwards it to the Python checks. Direct smoke-script runs can also set `DOCKER='sudo docker'`. These checks require Python 3.9+. Browser tests run with the deployed sandbox profile and Go's race detector. They cover rendering, context isolation, private redirects/subresources, readiness timeouts, slot bounds, process recovery, and the visual editor. The Compose smoke checks use temporary fixture projects and host directories. See [contributing](../CONTRIBUTING.md) for the complete validation workflow.

Live-site trials are opt-in checks whose source pages may change. With native Chromium installed, run them as a non-root user:

```sh
CHROMIUM_PATH=/usr/bin/chromium RSS_VISUAL_SITES=1 go test -run '^TestVisualSites$' -v ./internal/web
```

Set `RSS_SITE_FILTER=BBC` to limit the run to one site, or `RSS_SITE_ARTIFACTS=artifacts/browser` to save screenshots in an existing directory. These checks contact public sites and use disposable databases. The required checks use deterministic fixtures and do not depend on third-party sites.
