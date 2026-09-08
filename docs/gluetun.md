# Gluetun VPN

RSS Workshop can share an existing [Gluetun](https://github.com/qdm12/gluetun) container's network. Static requests and the app's local Chromium connections then use that network. SQLite requires no additional networking. A remote FlareSolverr browser uses the network of the machine or container running FlareSolverr.

This guide uses Gluetun managed separately on the same Docker host. Keep its VPN credentials in its own private configuration. If you need a new VPN container, first follow Gluetun's [provider setup instructions](https://github.com/qdm12/gluetun-wiki/tree/main/setup/providers) and verify that it connects successfully.

## Prepare the existing Gluetun service

Add a port mapping to the **Gluetun service's** Compose file, preserving its existing ports, VPN settings, and firewall configuration:

```yaml
services:
  gluetun:
    # Keep your existing image, VPN credentials, devices, and capabilities.
    ports:
      - "127.0.0.1:8080:8080"
```

This is a fragment to merge into your existing service, not a complete VPN deployment. Start with the port mapping alone, as in [Gluetun's port mapping guidance](https://github.com/qdm12/gluetun-wiki/blob/main/setup/port-mapping.md). Gluetun's [default firewall rules](https://github.com/qdm12/gluetun-wiki/blob/main/faq/firewall.md) already cover ordinary Docker-network access.

`FIREWALL_INPUT_PORTS` is an optional extra allowance for incoming ports on Gluetun's non-VPN interface. It is useful for network layouts the default rules do not cover, such as some Kubernetes sidecar setups. If Gluetun's firewall is blocking that access, append the app's internal port to this setting while preserving any existing entries. It does not create a Docker port mapping or VPN-side forwarding. Keep the app port out of `FIREWALL_VPN_INPUT_PORTS` and VPN port-forwarding rules. See Gluetun's [firewall options](https://github.com/qdm12/gluetun-wiki/blob/main/setup/options/firewall.md).

All containers sharing Gluetun share its listening ports. If another app already uses internal port `8080`, choose an unused port such as `8081`: map `127.0.0.1:8080:8081` and set `GLUETUN_APP_PORT=8081` below. If your setup needs the optional firewall allowance, update that to `8081` too. The public URL stays `http://localhost:8080` in that example. Gluetun documents this [shared network behavior](https://github.com/qdm12/gluetun-wiki/blob/main/setup/inter-containers-networking.md).

Recreate Gluetun through its own Compose project and wait for its VPN connection and health check to succeed. Coordinate this with any other apps already using it. Find its actual container name with `docker ps`; the example below assumes `gluetun`.

## Start RSS Workshop

From the RSS Workshop checkout, prepare `.env` and the host directory selected by `RSS_DATA_DIR` as described in [deployment](deployment.md). Keep an existing file, admin password, and data path. Add:

```dotenv
GLUETUN_CONTAINER=gluetun
GLUETUN_APP_PORT=8080
PUBLIC_BASE_URL=http://localhost:8080
```

The [override](../compose.gluetun.yaml) uses `network_mode: container:NAME` and removes the app's normal port publication. Gluetun must already be running; Compose cannot wait for the health of an external project. This follows [Gluetun's external-container setup](https://github.com/qdm12/gluetun-wiki/blob/main/setup/connect-a-container-to-gluetun.md).

```sh
docker inspect --format '{{.State.Health.Status}}' gluetun
docker compose -f compose.yaml -f compose.gluetun.yaml up -d --pull always --wait
docker compose -f compose.yaml -f compose.gluetun.yaml exec -T rss-workshop /rss-workshop -healthcheck
```

With `RSS_IMAGE` blank or unset, this pulls `ghcr.io/ldogg123/rss-workshop:v0.1.1-browser`. For static-only operation, add `-f compose.static.yaml` immediately after the base file; it defaults to `ghcr.io/ldogg123/rss-workshop:v0.1.1-static`. An explicit `RSS_IMAGE` must match the selected browser or static runtime. Use the same overrides for later operations. For a local source build, follow [build container images from source](deployment.md#build-container-images-from-source) and retain the Gluetun override.

Do not add `ports`, `networks`, or custom `dns` settings to RSS Workshop when it shares another container's network; configure networking on Gluetun instead. Docker documents the [restrictions of container network mode](https://docs.docker.com/engine/network/#container-networks).

Use the configured public URL to sign in. With a host reverse proxy, keep its upstream at the published host port. A containerized proxy on Gluetun's Docker network connects to `gluetun:8080` (or the chosen internal port). Set `PUBLIC_BASE_URL` to the real browser-facing HTTPS origin; it is not the VPN exit address.

## PostgreSQL and FlareSolverr

The app has Gluetun's network access, not a separate connection to RSS Workshop's default Compose network. The `postgres` service name in the PostgreSQL example will therefore need shared networking to remain reachable.

For a database in the same Docker network as Gluetun, add its service to that network. For example, with the supplied PostgreSQL override, create a private, ignored `compose.local.yaml`:

```yaml
services:
  postgres:
    networks:
      - vpn-services
networks:
  vpn-services:
    external: true
    name: YOUR_EXISTING_GLUETUN_NETWORK
```

Replace the network name with the one Gluetun actually uses, configure `POSTGRES_PASSWORD` and `DATABASE_URL`, and prepare the `POSTGRES_DATA_DIR` host directory from the [PostgreSQL guide](postgresql.md), then include every override:

```sh
docker compose -f compose.yaml -f compose.postgres.yaml -f compose.gluetun.yaml -f compose.local.yaml up -d --pull always --wait
```

For static-only operation, pair the `-static` image with `-f compose.static.yaml` after the base file. Keep PostgreSQL's host data mount and health check. Its port does not need to be published on the host or VPN. Gluetun 3.41 and newer supports resolving peers on its Docker network by service name; consult its [inter-container networking guide](https://github.com/qdm12/gluetun-wiki/blob/main/setup/inter-containers-networking.md) for your version.

For a database or solver outside that Docker network, configure a reachable address and narrowly scoped `FIREWALL_OUTBOUND_SUBNETS` exceptions on Gluetun where required. These destinations bypass the VPN; do not allow all networks or overlap the VPN tunnel range. Private DNS names may also require Gluetun's `DNS_REBINDING_PROTECTION_EXEMPT_HOSTNAMES`. Follow its [firewall documentation](https://github.com/qdm12/gluetun-wiki/blob/main/setup/options/firewall.md) for the actual network layout.

Database and solver service access does not require `ALLOW_CIDRS`. Leave it blank for public source pages. It only permits private sources to the guarded fetcher; it cannot change Docker routing or Gluetun's firewall.

To send FlareSolverr's source requests through the VPN, configure FlareSolverr to share Gluetun too. When both apps share the same Gluetun container, use `FLARESOLVERR_URL=http://127.0.0.1:8191` (or its configured port). Routing only RSS Workshop does not route a remote solver's browser.

## Verify and maintain

Check Gluetun's VPN health and reported exit address, sign in to RSS Workshop, and preview a public source with the desired fetch mode. The app's `/readyz` checks database readiness; it does not verify VPN egress. Saved RSS/Atom output can remain available during a fetch outage.

Keep Gluetun's firewall enabled. Its [firewall design](https://github.com/qdm12/gluetun-wiki/blob/main/faq/firewall.md) controls outgoing traffic when the tunnel is unavailable; the app does not implement a separate VPN kill switch. Verify routing and disconnect behavior with your provider and deployment before relying on it.

After Gluetun is **recreated or replaced**, recreate RSS Workshop as well so it joins the current container's network namespace:

```sh
docker compose -f compose.yaml -f compose.gluetun.yaml up -d --no-deps --no-build --force-recreate --wait rss-workshop
```

Include all the extra overrides used at startup. Do the same for other apps that share Gluetun, following their own deployment instructions. Do not switch RSS Workshop to ordinary bridge networking as a VPN recovery step.
