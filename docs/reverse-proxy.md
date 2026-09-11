# Reverse proxy

RSS Workshop publishes `127.0.0.1:8080` by default, which is reachable from the
host but not from the internet. Put it behind a TLS reverse proxy for remote
access. How you connect the two depends on where the proxy runs.

## A proxy installed on the host

Nothing to change. The proxy reaches the app on the published loopback port:

```caddyfile
rss.example.net {
	reverse_proxy 127.0.0.1:8080
}
```

Set `PUBLIC_BASE_URL=https://rss.example.net` in `.env` and recreate the
service.

## A proxy running in Docker

A container has its own network namespace, so `127.0.0.1` inside the proxy is
the proxy itself, not the host. Publishing a loopback port does not help. Put
both containers on a shared network instead:

```sh
docker network create proxy
docker compose -f compose.yaml -f compose.proxy.yaml up -d --pull always --wait
```

`compose.proxy.yaml` stops publishing a host port and joins the existing
`proxy` network, so the proxy reaches the app at `http://rss-workshop:8080`.
Set `PROXY_NETWORK` in `.env` if your network has another name. Apply it after
any runtime, database or storage override, as the last `-f`.

The app's own network stays in place, so an optional PostgreSQL service is
still reachable and is **not** joined to the proxy network.

### Caddy

```caddyfile
rss.example.net {
	reverse_proxy rss-workshop:8080
}
```

### Traefik

```yaml
services:
  rss-workshop:
    labels:
      traefik.enable: "true"
      traefik.http.routers.rss.rule: Host(`rss.example.net`)
      traefik.http.routers.rss.tls.certresolver: letsencrypt
      traefik.http.services.rss.loadbalancer.server.port: "8080"
```

Put those in your own `compose.local.yaml`, which is ignored by Git, rather
than editing the maintained files.

### nginx

```nginx
location / {
    proxy_pass http://rss-workshop:8080;
    proxy_set_header Host $host;
    proxy_set_header Origin $http_origin;
    proxy_read_timeout 160s;
}
```

## Required settings

**`PUBLIC_BASE_URL` must be the exact public origin**, such as
`https://rss.example.net`. It is not cosmetic: feed URLs are built from it, and
management requests are rejected unless the browser's `Origin` header matches
it. Forwarded headers do not replace it, and a path prefix is unsupported.

**Preserve the browser's `Origin` header.** A proxy that strips or rewrites it
makes every save, delete and refresh fail with a cross-origin rejection.

**Allow at least 160 seconds** for the response, so the longest FlareSolverr
timeout and its cleanup fit inside the proxy's own limit.

**Do not cache management or API responses.** The app sends `no-store` for
them; a proxy that overrides it can serve one visitor's session data to
another. Reader routes under `/feeds/` are safe to cache briefly and send their
own validators.

**Redact or disable access logging for `/feeds/`.** Reader links carry their
token in the URL path, so proxy access logs record credentials.

## Checking it worked

```sh
curl -sI https://rss.example.net/healthz
docker compose -f compose.yaml -f compose.proxy.yaml exec -T rss-workshop /rss-workshop -healthcheck
```

Sign in through the public address rather than a forwarded port: signing in
somewhere else produces a session whose origin does not match, and every
management action will be refused.
