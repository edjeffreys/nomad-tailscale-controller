# nomad-tailscale-controller

A controller that watches [Consul](https://www.consul.io/) for services tagged with `tailscale.enable=true` and automatically exposes them as [Tailscale VIP Services](https://tailscale.com/kb/1438/vip-services). Tag your services the same way you would for [Traefik](https://doc.traefik.io/traefik/providers/consul-catalog/) — one Tailscale node serves them all.

## How it works

```
┌────────────────┐       ┌────────────────┐       ┌────────────────┐
│ Consul Catalog │       │   Controller   │       │ Tailscale API  │
│                │◄──────│                │──────►│ (control plane)│
│ service: mealie│ block │ 1. Discover    │  PUT  │                │
│ tags:          │ query │    services    │ /vip- │ Creates svc:   │
│  - tailscale.  │       │ 2. Ensure VIP  │ svcs  │ mealie with    │
│    enable=true │       │    definitions │       │ auto-assigned  │
│                │       │ 3. Advertise   │       │ VIP address    │
│                │       │    services    │       │                │
│                │       │ 4. Apply serve │       │                │
│                │       │    config      │       │                │
└────────────────┘       └───────┬────────┘       └────────────────┘
                                 │
                                 │ PATCH /localapi/v0/prefs
                                 │  → AdvertiseServices
                                 │ POST /localapi/v0/serve-config
                                 │  → TCP forwarding rules
                                 ▼
                        ┌────────────────┐
                        │ Tailscale node │
                        │ (sidecar task) │
                        │                │
                        │ Registers as a │
                        │ service host & │
                        │ proxies traffic│
                        │ to backends    │
                        └────────────────┘
```

1. **Discover** — watches the Consul catalog (via blocking queries + poll fallback) for services tagged with `tailscale.enable=true`
2. **Ensure VIP Services** — auto-creates [Tailscale VIP Service](https://tailscale.com/kb/1438/vip-services) definitions via the control plane API (requires OAuth credentials)
3. **Advertise** — tells the local Tailscale node to register as a host for each managed service via `PATCH /localapi/v0/prefs`
4. **Apply serve config** — posts TCP forwarding rules to the local Tailscale daemon, mapping each service's port to its Consul backend address

One Tailscale node serves all your tagged services — no sidecar-per-service required.

## Service tags

Add these tags to any Consul-registered service (including Nomad `service` blocks which default to Consul):

```hcl
service {
  name = "mealie"
  port = "http"

  tags = [
    "tailscale.enable=true",
  ]
}
```

That's it. The controller will auto-create a Tailscale Service called `svc:mealie`, terminate TLS with Tailscale's auto-provisioned certificate, and proxy HTTPS traffic to the HTTP backend.

### Optional tags

| Tag | Default | Description |
|---|---|---|
| `tailscale.enable=true` | **(required)** | Opt-in to Tailscale exposure |
| `tailscale.hostname=X` | Consul service name | Override the Tailscale service name (`svc:X`) |
| `tailscale.proto=X` | `https` | Protocol mode: `https` (TLS termination + HTTP proxy) or `tcp` (raw TCP forwarding) |
| `tailscale.port=X` | `443` (https) / service port (tcp) | Override the frontend port |
| `tailscale.backend=host:port` | Consul instance address:port | Override the backend target |
| `tailscale.tag=tag:X` | `TS_DEFAULT_TAG` | Override the Tailscale ACL tag for this service |

### Example with overrides

```hcl
service {
  name = "mealie"
  port = "http"

  tags = [
    "tailscale.enable=true",
    "tailscale.hostname=recipes",       # exposed as svc:recipes
    "tailscale.tag=tag:web",            # use tag:web instead of the default tag:server
  ]
}
```

Works alongside other tag-based systems — Traefik tags are simply ignored:

```hcl
tags = [
  "tailscale.enable=true",
  "traefik.enable=true",
  "traefik.http.routers.mealie.rule=Host(`mealie.example.com`)",
]
```

### Protocol modes

**`https` (default)** — Tailscale terminates TLS using an auto-provisioned certificate and proxies HTTP to the backend. This is the right choice for most web services (the backend doesn't need its own TLS cert). Frontend port defaults to 443.

**`tcp`** — Raw TCP forwarding with no TLS termination. Use this for non-HTTP protocols (databases, game servers, etc.) or when the backend handles its own TLS. Frontend port defaults to the Consul service port.

```hcl
# Database: raw TCP on the actual port
tags = [
  "tailscale.enable=true",
  "tailscale.proto=tcp",
]
```

## Configuration

| Env var | Default | Description |
|---|---|---|
| `TAILNET` | **(required)** | Your tailnet domain (e.g. `tail5f17e.ts.net`) |
| `CONSUL_HTTP_ADDR` | `http://localhost:8500` | Consul agent address |
| `CONSUL_HTTP_TOKEN` | | Consul ACL token (if ACLs are enabled) |
| `TAILSCALE_SOCKET` | `/var/run/tailscale/tailscaled.sock` | Path to the Tailscale daemon socket |
| `TS_OAUTH_CLIENT_ID` | | Tailscale OAuth client ID (enables auto-creation of VIP services) |
| `TS_OAUTH_CLIENT_SECRET` | | Tailscale OAuth client secret |
| `TS_DEFAULT_TAG` | `tag:server` | Default ACL tag applied to auto-created services |
| `POLL_INTERVAL` | `10s` | Fallback poll interval (Consul blocking queries provide real-time updates) |
| `TAG_PREFIX` | `tailscale.` | Tag prefix to look for on Consul services |
| `LOG_LEVEL` | `info` | Set to `debug` for verbose logging |

### Tailscale OAuth setup

To enable automatic VIP service creation, create an OAuth client in the [Tailscale admin console](https://login.tailscale.com/admin/settings/oauth):

1. Go to **Settings → OAuth clients → Generate OAuth client**
2. Grant the **Services: Write** scope
3. Store the client ID and secret securely (e.g. in Nomad variables)

Without OAuth credentials, the controller runs in **local-only mode** — it still advertises services and configures the Tailscale node's serve config, but you'll need to manually create VIP service definitions in the admin console.

### Tailscale ACL auto-approvers

To avoid manually approving each service host, add an auto-approver rule to your [Tailscale ACL policy](https://login.tailscale.com/admin/acls):

```json
{
  "autoApprovers": {
    "services": {
      "svc:*": ["tag:server"]
    }
  }
}
```

## Deployment

### 1. Store secrets in Nomad variables

```bash
nomad var put nomad/jobs/tailscale-controller \
  authkey="tskey-auth-xxx" \
  oauth_client_id="your-oauth-client-id" \
  oauth_client_secret="your-oauth-client-secret"
```

### 2. Create a CSI volume for Tailscale state persistence

```hcl
resource "nomad_csi_volume_registration" "tailscale_controller" {
  plugin_id   = "nfs"
  volume_id   = "tailscale-controller"
  name        = "tailscale-controller"
  external_id = "tailscale-controller"
  namespace   = "tailscale"

  capability {
    access_mode     = "multi-node-multi-writer"
    attachment_mode = "file-system"
  }
}
```

### 3. Deploy the controller

```bash
nomad job run controller.nomad.hcl
```

### 4. Tag your services

Add `tailscale.enable=true` to any Consul-registered service and redeploy — the controller will pick it up within seconds (via blocking query) or at the next poll interval.

## Architecture

The controller runs as a Nomad job with two tasks in the same group:

- **tailscale** (sidecar) — a Tailscale node that registers as a service host and proxies traffic to backends. Shares its daemon socket with the controller via Nomad's `/alloc/tmp/` directory.
- **controller** — watches Consul, manages VIP service definitions, advertises services, and applies TCP forwarding rules to the Tailscale node.

Traffic flows: **Tailscale client → Tailscale VIP → Tailscale node → Consul service backend**

## Development

```bash
go build ./...
go test ./...
go vet ./...
```
