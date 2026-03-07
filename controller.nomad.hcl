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

    # The controller needs a Tailscale sidecar of its own —
    # this is the single Tailscale node that serves all arr services.
    volume "tailscale-state" {
      type            = "csi"
      source          = "tailscale-controller"
      read_only       = false
      attachment_mode = "file-system"
      access_mode     = "multi-node-multi-writer"
    }

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
        TS_HOSTNAME  = "arr-ingress"
        TS_STATE_DIR = "/var/lib/tailscale"
        TS_USERSPACE = "true"
        TS_SOCKET    = "/alloc/tmp/tailscaled.sock"
      }

      template {
        data        = <<EOF
{{ with nomadVar "jobs/tailscale-controller" }}TS_AUTHKEY={{ .authkey | trimSpace }}{{ end }}
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
        NOMAD_ADDR        = "http://172.17.0.1:4646"
        TAILNET           = "tail5f17e.ts.net"
        NOMAD_NAMESPACES  = "*"
        POLL_INTERVAL     = "30s"
        TAG_PREFIX        = "tailscale."
        TAILSCALE_SOCKET  = "/alloc/tmp/tailscaled.sock"
      }

      template {
        data        = <<EOF
{{ with nomadVar "jobs/tailscale-controller" }}NOMAD_TOKEN={{ .nomad_token | trimSpace }}{{ end }}
EOF
        destination = "secrets/nomad.env"
        env         = true
      }

      resources {
        cpu    = 50
        memory = 64
      }
    }
  }
}
