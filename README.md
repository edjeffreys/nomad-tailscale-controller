# nomad-tailscale-controller

A lightweight controller that watches Nomad services and dynamically manages a Tailscale serve config — similar to how Traefik uses tags for routing, but for your Tailscale ingress.

## How it works

1. The controller watches the Nomad event stream (with polling fallback) for services tagged with `tailscale.*`
2. When it finds services with `tailscale.enable=true`, it builds a Tailscale serve config
3. It applies the config via `tailscale serve --config` on the ingress node
4. One Tailscale node (`arr-ingress`) serves all your private services — rather than a sidecar per service

## Service tags

Add these tags to any Nomad service to expose it via Tailscale:

```hcl
service {
  name = "whoami"
  port = "whoami-http"

  tags = [
    "tailscale.enable=true",
    "tailscale.hostname=whoami",   # optional, defaults to service name
    "tailscale.port=443",           # optional, defaults to 443
    # tailscale.backend defaults to <service>.service.consul:<port>
    # override if needed:
    # "tailscale.backend=whoami.service.consul:8080",
  ]
}
```

This produces a serve config entry:
```json
{
  "Web": {
    "whoami.tail5f17e.ts.net:443": {
      "Handlers": { "/": { "Proxy": "http://whoami.service.consul:8080" } }
    }
  }
}
```

## Configuration

| Env var | Default | Description |
|---|---|---|
| `TAILNET` | required | Your tailnet domain e.g. `tail5f17e.ts.net` |
| `NOMAD_ADDR` | `http://localhost:4646` | Nomad API address |
| `NOMAD_TOKEN` | | Nomad ACL token |
| `NOMAD_NAMESPACES` | `*` | Comma-separated namespaces to watch |
| `TAILSCALE_SOCKET` | `/var/run/tailscale/tailscaled.sock` | Tailscale daemon socket |
| `POLL_INTERVAL` | `10s` | Fallback poll interval |
| `TAG_PREFIX` | `tailscale.` | Tag prefix to look for |

## Deployment

1. Store your Tailscale auth key and Nomad token in Nomad variables:
```bash
nomad var put -namespace infra jobs/tailscale-controller \
  authkey=tskey-client-xxx \
  nomad_token=your-nomad-token
```

2. Create the NFS volume for Tailscale state:
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

3. Deploy the controller:
```bash
nomad job run deploy/controller.nomad.hcl
```

4. Tag your services and redeploy them — the controller will pick them up within one poll interval.

## Consul intentions

Since traffic now flows from `tailscale-controller` → Consul service rather than per-service Tailscale sidecars, update your intentions:

```hcl
resource "consul_config_entry" "whoami" {
  name = "whoami"
  kind = "service-intentions"

  config_json = jsonencode({
    Sources = [
      { Name = "tailscale-controller", Action = "allow" },
      { Name = "*",                    Action = "deny"  },
    ]
  })
}
```
