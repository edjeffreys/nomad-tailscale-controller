job "tailscale-controller" {
  datacenters = ["dc1"]
  type        = "service"
  namespace   = "tailscale"

  group "controller" {
    count = 1

    network {
      mode = "bridge"

      dns {
        servers = ["172.17.0.1"]
      }
    }

    volume "tailscale-state" {
      type            = "csi"
      source          = "tailscale-controller"
      read_only       = false
      attachment_mode = "file-system"
      access_mode     = "multi-node-multi-writer"
    }

    # Tailscale sidecar — the single node that advertises all managed services.
    # Shares its daemon socket with the controller via /alloc/tmp/.
    task "tailscale" {
      driver = "docker"

      lifecycle {
        hook    = "prestart"
        sidecar = true
      }

      config {
        image = "tailscale/tailscale:v1.94.2"
      }

      env {
        TS_HOSTNAME   = "nomad-ingress"
        TS_STATE_DIR  = "/var/lib/tailscale"
        TS_USERSPACE  = "true"
        TS_SOCKET     = "/alloc/tmp/tailscaled.sock"
        TS_EXTRA_ARGS = "--advertise-tags=tag:server"
      }

      template {
        data        = <<EOF
{{ with nomadVar "nomad/jobs/tailscale-controller" }}TS_AUTHKEY={{ .authkey | trimSpace }}{{ end }}
EOF
        destination = "secrets/tailscale.env"
        env         = true
      }

      volume_mount {
        volume      = "tailscale-state"
        destination = "/var/lib/tailscale"
      }

      resources {
        cpu    = 50
        memory = 128
      }
    }

    task "controller" {
      driver = "docker"

      config {
        image = "ghcr.io/edjeffreys/nomad-tailscale-controller:latest"
      }

      env {
        CONSUL_HTTP_ADDR = "http://172.17.0.1:8500"
        TAILNET          = "tail5f17e.ts.net"
        POLL_INTERVAL    = "30s"
        TAG_PREFIX       = "tailscale."
        TAILSCALE_SOCKET = "/alloc/tmp/tailscaled.sock"
        TS_DEFAULT_TAG   = "tag:server"
      }

      template {
        data        = <<EOF
{{ with nomadVar "nomad/jobs/tailscale-controller" }}
TS_OAUTH_CLIENT_ID={{ .oauth_client_id | trimSpace }}
TS_OAUTH_CLIENT_SECRET={{ .oauth_client_secret | trimSpace }}
{{ end }}
EOF
        destination = "secrets/ts-oauth.env"
        env         = true
      }

      resources {
        cpu    = 50
        memory = 64
      }
    }
  }
}
