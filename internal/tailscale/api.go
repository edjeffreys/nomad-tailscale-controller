package tailscale

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/oauth2/clientcredentials"
)

const apiBase = "https://api.tailscale.com/api/v2"

// VIPService represents a Tailscale VIP Service definition in the control plane.
// Matches the format used by the Tailscale API and k8s operator.
type VIPService struct {
	Name        string            `json:"name"`
	Addrs       []string          `json:"addrs,omitempty"`
	Comment     string            `json:"comment,omitempty"`
	Ports       []string          `json:"ports"`
	Tags        []string          `json:"tags,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// APIClient manages Tailscale Services via the control plane REST API.
type APIClient struct {
	tailnet string
	http    *http.Client
	logger  *zap.Logger
}

// NewAPIClient creates a client that authenticates with OAuth2 client credentials.
func NewAPIClient(tailnet, clientID, clientSecret string, logger *zap.Logger) *APIClient {
	oauthConfig := &clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     apiBase + "/oauth/token",
	}

	return &APIClient{
		tailnet: tailnet,
		http:    oauthConfig.Client(context.Background()),
		logger:  logger,
	}
}

// EnsureService creates a Tailscale VIP Service definition if it doesn't already exist.
// Uses GET to check existence first, only PUTs to create new services.
func (a *APIClient) EnsureService(ctx context.Context, svc VIPService) error {
	name := svc.Name
	if !strings.HasPrefix(name, "svc:") {
		name = "svc:" + name
		svc.Name = name
	}

	// Check if the service already exists
	exists, err := a.getService(ctx, name)
	if err != nil {
		a.logger.Debug("could not check existing service, attempting create", zap.String("service", name), zap.Error(err))
	}
	if exists != nil {
		a.logger.Debug("service already exists in control plane", zap.String("service", name))
		return nil
	}

	data, err := json.Marshal(svc)
	if err != nil {
		return fmt.Errorf("failed to marshal service: %w", err)
	}

	url := fmt.Sprintf("%s/tailnet/%s/vip-services/%s", apiBase, a.tailnet, name)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("failed to PUT service %s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API returned %d for service %s: %s", resp.StatusCode, name, body)
	}

	a.logger.Info("created tailscale service", zap.String("service", name))
	return nil
}

// getService fetches a single VIP Service by name. Returns nil if not found.
func (a *APIClient) getService(ctx context.Context, name string) (*VIPService, error) {
	url := fmt.Sprintf("%s/tailnet/%s/vip-services/%s", apiBase, a.tailnet, name)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, body)
	}

	var svc VIPService
	if err := json.NewDecoder(resp.Body).Decode(&svc); err != nil {
		return nil, err
	}
	return &svc, nil
}

// ListServices returns all Tailscale VIP Services defined in the tailnet.
func (a *APIClient) ListServices(ctx context.Context) ([]VIPService, error) {
	url := fmt.Sprintf("%s/tailnet/%s/vip-services", apiBase, a.tailnet)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := a.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to list services: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Services []VIPService `json:"services"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode services: %w", err)
	}

	return result.Services, nil
}
