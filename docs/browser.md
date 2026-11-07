# Chromium rendering

The default Docker deployment includes Chromium for JavaScript rendering. It reuses one managed Chromium process and creates a new incognito context for each render. Cookies and local storage are discarded afterward. Rendered pages use the same extraction and sanitization as static HTML.

## Deploy

```sh
# Configure .env and prepare the host data directory first; see deployment.
docker compose up -d --pull always --wait
```

This pulls `ghcr.io/ldogg123/rss-workshop:latest` when `RSS_IMAGE` is blank or unset. An explicit `RSS_IMAGE` must select a browser image for this setup. See [deployment](deployment.md) for credentials and public URLs. Use `sudo docker` if your account requires it.

For a local source build, use the explicit [container build overrides](deployment.md#build-container-images-from-source). The [browser Dockerfile](../Dockerfile.browser) pins the Debian base digest and Chromium package version; maintainers should review those pins for security updates before rebuilding. `CHROMIUM_VERSION` is a build argument for upgrades.

For a smaller runtime without local Chromium, use the [static override](../compose.static.yaml):

```sh
docker compose -f compose.yaml -f compose.static.yaml up -d --pull always --wait
```

With `RSS_IMAGE` blank or unset, this pulls `ghcr.io/ldogg123/rss-workshop:latest-static`. If you set `RSS_IMAGE` explicitly, select a static image and keep this override on later runs. This keeps the same host data directory and supports static HTTP fetching and external FlareSolverr. It clears Chromium configuration and replaces the browser's sandbox and resource settings with the static runtime's settings. Saved Chromium recipes need the browser runtime to refresh successfully.

For native installations, set `CHROMIUM_PATH` to an installed Chromium executable and run the app as a non-root user on a host that supports its sandbox. Docker deployments do not require host Chromium or Node.

## Recipe settings

- **Static HTTP:** fetch and extract HTML without JavaScript. A page in another encoding, such as windows-1252, Shift_JIS or UTF-16, is decoded to UTF-8 as a browser decodes it: from a byte order mark, else the `charset` in its `Content-Type` header, else a `<meta>` charset within its first 1,024 bytes; a [feed source](feed-sources.md) reads its XML declaration there instead. A page that declares none, or names an encoding browsers do not support, is read as UTF-8, as is a page that declares a legacy encoding but whose non-ASCII text is mostly valid UTF-8, which usually means the site moved to UTF-8 and kept the old declaration. A few invalid bytes, such as a character cut in half by an excerpt, do not change that. List pages, article pages and the visual selector's static snapshot are decoded the same way, and send the same [User-Agent](deployment.md#identifying-requests), which `FETCH_USER_AGENT` or the feed's own **User-Agent** can replace.
- **Chromium:** always render, then extract the resulting DOM. Rendered pages send Chromium's own User-Agent and language preferences, which neither `FETCH_USER_AGENT` nor the feed's **User-Agent** or **Accept-Language** changes.
- **Auto:** try static first. Render once only when a successful HTTP response yields zero valid items. HTTP failures, access blocks, and invalid selectors do not trigger browser retries.
- **FlareSolverr (Cloudflare):** use a separately configured external browser service for challenge pages. This mode does not require local Chromium and is never an Auto fallback. Some CAPTCHAs remain unsupported. See [FlareSolverr setup and network limits](flaresolverr.md).
- **Wait for element:** an optional CSS selector that must exist in the local Chromium page before extraction. This is CSS even for XPath extraction recipes; it does not apply to FlareSolverr.
- **Extra render wait:** an optional 0–5000 ms settling delay after local Chromium navigation/readiness. Prefer a specific readiness selector for pages that load asynchronously. This delay does not apply to FlareSolverr.
- **User-Agent:** under **Request settings**, an optional User-Agent for this feed's static requests: its list page in Static HTTP mode and in Auto before any render, article pages fetched as plain HTML, previews, and the visual selector's Static HTML snapshot. It replaces `FETCH_USER_AGENT` and the built-in name for this feed only. Blank uses the server's default User-Agent. Surrounding spaces are trimmed, and a value longer than 256 characters or with anything but printable ASCII is refused. Pages that Chromium renders or FlareSolverr loads keep their browser's own. See [identifying requests](deployment.md#identifying-requests).
- **Accept-Language:** under **Request settings**, the languages to ask the source for, most preferred first, such as `en` or `de-CH, de;q=0.9, en;q=0.5`. It is sent as the `Accept-Language` header with the same static requests as the feed's User-Agent, and only when set. There is no server-wide setting, so a feed without one sends no `Accept-Language`. Pages that Chromium renders or FlareSolverr loads keep their browser's own language preferences. A site that offers English may answer `en` with English dates, which RSS Workshop [recognizes](dates.md#month-names) where other languages are not, and some sites refuse requests that state no language at all. Surrounding spaces are trimmed. A value is refused unless it lists at most 10 language ranges in at most 128 characters, each `*` or a language tag such as `pt-BR`, optionally weighted from `;q=0` to `;q=1`.

  Saving a different value is a recipe edit like the User-Agent: it drops the source's conditional-request validators and any refresh in progress, and publishes nothing by itself. If the source answers in the new language, its stories change at the next refresh. A story is updated in place only while what identifies it stays the same: its link, or its title in a recipe without a link selector, which uses a [title-based identity](visual-selector.md#manual-selectors-and-images). On a site whose story links contain the language, such as `/de/…` and `/en/…`, or in a recipe without a link selector whose titles are translated, every story appears once more under a new GUID, and the copies in the old language stay until [`MAX_ITEMS`](deployment.md#environment-variables) retention removes them.

  Saved [article bodies](atom.md#full-article-content) keep the language they were fetched in. To fetch them again, clear **Article body**, save, then restore it and save again. Later refreshes fetch 10 per refresh for stories still on the list page, but stories no longer listed lose their article for good and show their list-page description, so on a feed with a long history it may be better to keep the older articles in their language.

Changing fetch settings invalidates source validators and in-flight results. Browser-derived results do not retain static HTTP validators, so Auto cannot mistake a source 304 for proof that rendered content is unchanged.

[Story filters](filtering.md) apply after extraction in every fetch mode. If valid stories exist but your rules exclude them all, Auto succeeds with zero included stories and does not render again.

## Capacity

`BROWSER_SLOTS` defaults to 2 and permits 1–8 pages rendering at once. Every Chromium render shares these slots: list pages, [article pages](atom.md#full-article-content), Auto fallbacks, previews and the visual selector's page loads. Each of those jobs also holds one of the `STATIC_WORKERS` refresh/preview slots shared by every fetch mode, so a render waiting for Chromium never queues without bound. The authenticated dashboard reports browser capacity and current activity. One additional blank control tab belongs to the managed process.

RSS Workshop starts a scheduled refresh that renders in Chromium only when a slot is free: a feed in Chromium mode, and a feed in any mode but FlareSolverr, Auto included, that renders its article pages. When more such feeds are due than there are slots, as after a restart, an outage or an import, the rest stay due and start oldest first as slots free up. Waiting for a slot is not a failure: it records no error, does not back a feed off and sends no [notification](operations.md#notifications). Other feeds are not held back. A Chromium-mode feed whose article pages are plain HTML, the default, gives its slot back once its list page is rendered, before it fetches them. A feed that renders its article pages holds its slot from its start, through a static list fetch too, and renders them one at a time, so it holds a single slot until the last one; with many new stories, some of the 10 it may fetch per refresh can be left for the next refresh.

**Auto** feeds whose article pages are plain HTML start without waiting for a slot. Most find their stories as static HTML and never render, and holding a slot through every static fetch would let only `BROWSER_SLOTS` of them run at once. A fallback render instead waits for a free slot inside the feed's `FETCH_TIMEOUT`. A feed that always falls back is better set to Chromium mode, which waits for a slot before it starts.

**Preview items** and **Choose elements visually** are never refused for want of a Chromium slot, unlike [FlareSolverr](flaresolverr.md#capacity): a Chromium render can be cancelled, and frees its slot as soon as it ends. They take a free slot when there is one, so a refresh due meanwhile stays due until they finish. Otherwise they wait for one inside their own deadline. A preview of a feed that renders its article pages fetches its three sample articles one at a time.

A render that cannot get a slot within its deadline fails with `no free Chromium slot before the deadline`. A refresh in Chromium mode, or one that renders its article pages, meets this only when renders that started without a slot (Auto fallbacks, previews and page loads) kept the pool busy for its whole `FETCH_TIMEOUT`. An Auto fallback meets it whenever other renders, scheduled refreshes included, keep every slot busy for the rest of its deadline. Either way the refresh fails and backs off as usual. If it recurs, raise `BROWSER_SLOTS` where the host has the memory for more renders, or set frequently falling-back Auto feeds to Chromium mode.

## Bounds and recovery

The existing `FETCH_TIMEOUT` bounds each refresh/preview, including readiness and settling. Browser initialization has its own 15-second watchdog. Tabs and contexts are disposed on success, error, cancellation, and timeout. A crashed process fails its active jobs and is recreated for later jobs. The process recycles after 100 completed renders when no renders are active. Downloads are denied and popup targets are closed.

Rendered snapshots are limited to 4 MiB before the regular extraction limits apply, like static pages, whose limit applies to both the response and the page once decoded to UTF-8. The proxy caps active connections at 64, ordinary HTTP response streams at 16 MiB, and each direction of CONNECT tunnels at 32 MiB with a two-minute lifetime. These limits may reject resource-heavy sites. The browser container limits memory to 2 GiB and process count to 256, with 256 MiB shared memory and 256 MiB temporary storage.

## Sandbox and outbound policy

The image runs as UID/GID 65532 with no Linux capabilities, a read-only root filesystem, and `no-new-privileges`. Chromium's sandbox stays enabled; the application refuses to launch it as root. The seccomp profile starts from Docker/Moby's standard profile and additionally allows `clone`, `unshare`, `setns`, and `chroot`, enabling Chromium to establish its own nested user/PID/network namespaces and chroot. All other profile restrictions remain. No privileged mode, host-wide AppArmor relaxation, or `--no-sandbox` flag is used.

The [seccomp profile](../deploy/chromium-seccomp.json) derives from [Moby's default profile](https://github.com/moby/profiles/blob/main/seccomp/default.json), with its [license preserved](licenses/Moby-profiles-LICENSE). Integration tests assert PID namespaces, network namespaces, and Seccomp-BPF. Hosts that forbid unprivileged user namespaces need a sandbox-compatible configuration; the app does not silently disable sandboxing.

Chromium uses a loopback-only HTTP proxy in the Go process. The proxy resolves and validates every actual TCP destination using the same IP policy and allowlist as static fetching, then dials the checked IP directly. That covers navigations, redirects, HTTP subresources, and HTTPS CONNECT destinations without a second DNS lookup. Implicit loopback proxy bypass is removed. CONNECT is restricted to port 443; HTTPS on other ports is currently unsupported. QUIC and non-proxied WebRTC UDP are disabled. WebSockets are not blocked, because DevTools URL patterns do not stop them and a proxy that sees only `CONNECT host:443` cannot tell `wss://` from HTTPS. Both `ws://` and `wss://` reach the proxy as CONNECT, so a rendered page can open one only to a validated destination on port 443, within the connection, size and lifetime limits above. Source credentials and bearer feed tokens are not logged by the proxy.

This is application-level outbound enforcement, not a host packet firewall. HTTPS payloads stay encrypted end to end and are not inspected by the proxy. Chromium's broker process still has the container's network access; an engine exploit or a protocol path that ignores browser proxy settings is outside this enforcement guarantee. No claim is made that these flags prevent every possible non-HTTP network path. Use a dedicated host/container network boundary if the broker itself must be unable to reach internal networks. ALLOW_CIDRS is an explicit administrator exception and should be narrowly scoped. Tests cover private HTTP subresources and redirects, WebRTC UDP to a non-loopback address, WebSockets to rejected destinations and other ports, plus direct proxy CONNECT policy; they do not prove universal network isolation.

## Validation

```sh
make browser-test
make docker-smoke
```

Use `DOCKER='sudo docker'` with Make when needed; the smoke target forwards it to the Python checks. Direct smoke-script runs can also set `DOCKER='sudo docker'`. These checks require Python 3.11+. Browser tests run with the deployed sandbox profile and Go's race detector. They cover rendering, context isolation, private redirects/subresources, WebRTC UDP, WebSockets, readiness timeouts, slot bounds, process recovery, and the visual editor. `make check` covers the scheduler's [Chromium admission](#capacity) without Chromium, through a stand-in for the pool and the pool's own slot wait. The WebRTC test needs a non-loopback interface, because a loopback-only network produces no ICE activity, and fails rather than passing without one. The Compose smoke checks use temporary fixture projects and host directories. See [contributing](../CONTRIBUTING.md) for the complete validation workflow.

Live-site trials are opt-in checks whose source pages may change. With native Chromium installed, run them as a non-root user:

```sh
CHROMIUM_PATH=/usr/bin/chromium RSS_VISUAL_SITES=1 go test -run '^TestVisualSites$' -v ./internal/web
```

Set `RSS_SITE_FILTER=BBC` to limit the run to one site, or `RSS_SITE_ARTIFACTS=artifacts/browser` to save screenshots in an existing directory. These checks contact public sites and use disposable databases. The required checks use deterministic fixtures and do not depend on third-party sites.

### Documentation screenshots

The README images come from `TestBrowserUIDocumentation`, which uses fictional stories, example.com URLs, and a disposable database. With native Chromium installed, capture fresh images as a non-root user:

```sh
mkdir -p artifacts/browser
CHROMIUM_PATH=/usr/bin/chromium RSS_UI_DOCS=1 RSS_SITE_ARTIFACTS="$PWD/artifacts/browser" \
  go test -run '^TestBrowserUIDocumentation$' -v ./internal/web
```

The `docs-*.png` files are written to the artifact directory. Inspect them before copying `docs-dashboard`, `docs-visual-selector`, `docs-filters`, `docs-filter-library`, `docs-full-content` and `docs-diagnostics` into `docs/screenshots/` without the `docs-` prefix; `docs-dashboard-light` is not used in the README. Keep operator data, credentials, and reader links out of documentation captures. The fixture also verifies that its 100-phrase example includes two of its three stories.
