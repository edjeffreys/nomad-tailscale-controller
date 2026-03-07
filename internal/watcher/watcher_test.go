package watcher

import (
	"testing"
)

func TestParseTags(t *testing.T) {
	tests := []struct {
		name   string
		tags   []string
		prefix string
		want   map[string]string
	}{
		{
			name:   "enable only",
			tags:   []string{"tailscale.enable=true"},
			prefix: "tailscale.",
			want:   map[string]string{"enable": "true"},
		},
		{
			name:   "enable without value defaults to true",
			tags:   []string{"tailscale.enable"},
			prefix: "tailscale.",
			want:   map[string]string{"enable": "true"},
		},
		{
			name:   "multiple tags",
			tags:   []string{"tailscale.enable=true", "tailscale.hostname=myapp", "tailscale.port=8443"},
			prefix: "tailscale.",
			want:   map[string]string{"enable": "true", "hostname": "myapp", "port": "8443"},
		},
		{
			name:   "ignores non-prefixed tags",
			tags:   []string{"version=1.0", "tailscale.enable=true", "env=prod"},
			prefix: "tailscale.",
			want:   map[string]string{"enable": "true"},
		},
		{
			name:   "custom prefix",
			tags:   []string{"ts.enable=true", "ts.hostname=foo"},
			prefix: "ts.",
			want:   map[string]string{"enable": "true", "hostname": "foo"},
		},
		{
			name:   "empty tags",
			tags:   []string{},
			prefix: "tailscale.",
			want:   map[string]string{},
		},
		{
			name:   "nil tags",
			tags:   nil,
			prefix: "tailscale.",
			want:   map[string]string{},
		},
		{
			name:   "value with equals sign",
			tags:   []string{"tailscale.backend=http://host:8080/path?a=b"},
			prefix: "tailscale.",
			want:   map[string]string{"backend": "http://host:8080/path?a=b"},
		},
		{
			name:   "enable set to false",
			tags:   []string{"tailscale.enable=false"},
			prefix: "tailscale.",
			want:   map[string]string{"enable": "false"},
		},
		{
			name:   "mixed traefik and tailscale tags",
			tags: []string{
				"traefik.enable=true",
				"traefik.http.routers.mealie.rule=Host(`mealie.skaal.dev`)",
				"tailscale.enable=true",
				"tailscale.hostname=mealie",
			},
			prefix: "tailscale.",
			want:   map[string]string{"enable": "true", "hostname": "mealie"},
		},
		{
			name:   "tag override",
			tags:   []string{"tailscale.enable=true", "tailscale.tag=tag:web"},
			prefix: "tailscale.",
			want:   map[string]string{"enable": "true", "tag": "tag:web"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTags(tt.tags, tt.prefix)
			if len(got) != len(tt.want) {
				t.Fatalf("parseTags() returned %d entries, want %d\ngot:  %v\nwant: %v", len(got), len(tt.want), got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("parseTags()[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}
