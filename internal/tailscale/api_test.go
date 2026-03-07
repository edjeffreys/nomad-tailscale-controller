package tailscale

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

func TestEnsureService_CreateNew(t *testing.T) {
	var receivedMethod string
	var receivedPath string
	var receivedBody VIPService

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &receivedBody)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)

	client := &APIClient{
		tailnet: "test.ts.net",
		http:    srv.Client(),
		logger:  zap.NewNop(),
	}
	// Override apiBase for tests
	origBase := apiBase
	defer func() {}()

	// Use the test server URL directly by making a client that rewrites URLs
	client.http = &http.Client{
		Transport: &apiRewriteTransport{base: srv.Client().Transport, target: srv.URL},
	}

	svc := VIPService{
		Name:    "svc:mealie",
		Comment: "Managed by nomad-tailscale-controller",
		Ports:   []string{"tcp:443"},
		Tags:    []string{"tag:server"},
	}

	err := client.EnsureService(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if receivedMethod != http.MethodPut {
		t.Errorf("method = %s, want PUT", receivedMethod)
	}
	if receivedPath != "/api/v2/tailnet/test.ts.net/vip-services/svc:mealie" {
		t.Errorf("path = %s, want /api/v2/tailnet/test.ts.net/vip-services/svc:mealie", receivedPath)
	}
	if receivedBody.Name != "svc:mealie" {
		t.Errorf("body name = %s, want svc:mealie", receivedBody.Name)
	}
	_ = origBase
}

func TestEnsureService_AddsSvcPrefix(t *testing.T) {
	var receivedBody VIPService
	var receivedPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client := &APIClient{
		tailnet: "test.ts.net",
		http:    &http.Client{Transport: &apiRewriteTransport{base: srv.Client().Transport, target: srv.URL}},
		logger:  zap.NewNop(),
	}

	// Name without svc: prefix — should be added automatically
	svc := VIPService{
		Name:  "mealie",
		Ports: []string{"tcp:443"},
	}

	err := client.EnsureService(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if receivedPath != "/api/v2/tailnet/test.ts.net/vip-services/svc:mealie" {
		t.Errorf("path = %s, want svc: prefix in URL", receivedPath)
	}
	if receivedBody.Name != "svc:mealie" {
		t.Errorf("body name = %s, want svc:mealie", receivedBody.Name)
	}
}

func TestEnsureService_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("insufficient permissions"))
	}))
	t.Cleanup(srv.Close)

	client := &APIClient{
		tailnet: "test.ts.net",
		http:    &http.Client{Transport: &apiRewriteTransport{base: srv.Client().Transport, target: srv.URL}},
		logger:  zap.NewNop(),
	}

	err := client.EnsureService(context.Background(), VIPService{Name: "svc:test", Ports: []string{"tcp:443"}})
	if err == nil {
		t.Fatal("expected error for 403")
	}
	if !contains(err.Error(), "403") {
		t.Errorf("error should mention 403, got: %s", err)
	}
}

func TestListServices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/v2/tailnet/test.ts.net/vip-services" {
			t.Errorf("path = %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string][]VIPService{
			"services": {
				{Name: "svc:mealie", Ports: []string{"tcp:443"}, Tags: []string{"tag:server"}},
				{Name: "svc:sonarr", Ports: []string{"tcp:443"}},
			},
		})
	}))
	t.Cleanup(srv.Close)

	client := &APIClient{
		tailnet: "test.ts.net",
		http:    &http.Client{Transport: &apiRewriteTransport{base: srv.Client().Transport, target: srv.URL}},
		logger:  zap.NewNop(),
	}

	services, err := client.ListServices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 2 {
		t.Fatalf("got %d services, want 2", len(services))
	}
	if services[0].Name != "svc:mealie" {
		t.Errorf("first service = %s, want svc:mealie", services[0].Name)
	}
}

func TestListServices_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	t.Cleanup(srv.Close)

	client := &APIClient{
		tailnet: "test.ts.net",
		http:    &http.Client{Transport: &apiRewriteTransport{base: srv.Client().Transport, target: srv.URL}},
		logger:  zap.NewNop(),
	}

	_, err := client.ListServices(context.Background())
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if !contains(err.Error(), "500") {
		t.Errorf("error should mention 500, got: %s", err)
	}
}

func TestVIPServiceJSON(t *testing.T) {
	svc := VIPService{
		Name:    "svc:mealie",
		Comment: "test",
		Ports:   []string{"tcp:443"},
		Tags:    []string{"tag:server"},
		Annotations: map[string]string{
			"nomad-tailscale-controller/managed": "true",
		},
	}

	data, err := json.Marshal(svc)
	if err != nil {
		t.Fatal(err)
	}

	var decoded VIPService
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Name != "svc:mealie" {
		t.Error("name should survive round-trip")
	}
	if len(decoded.Ports) != 1 || decoded.Ports[0] != "tcp:443" {
		t.Error("ports should survive round-trip")
	}
	if decoded.Annotations["nomad-tailscale-controller/managed"] != "true" {
		t.Error("annotations should survive round-trip")
	}
}

func TestVIPServiceJSON_OmitsEmptyAddrs(t *testing.T) {
	svc := VIPService{
		Name:  "svc:test",
		Ports: []string{"tcp:443"},
	}

	data, err := json.Marshal(svc)
	if err != nil {
		t.Fatal(err)
	}

	jsonStr := string(data)
	if contains(jsonStr, `"addrs"`) {
		t.Error("addrs should be omitted when empty")
	}
}

// apiRewriteTransport rewrites requests from the production API URL to a test server.
type apiRewriteTransport struct {
	base   http.RoundTripper
	target string
}

func (t *apiRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = "http"
	req.URL.Host = t.target[len("http://"):]
	return t.base.RoundTrip(req)
}
