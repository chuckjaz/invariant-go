package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"invariant/internal/httputil"
)

// Assert that ControlPlaneClient implements ControlPlane
var _ ControlPlane = (*ControlPlaneClient)(nil)

// ControlPlaneClient communicates with the itp-v1 Control Plane endpoints.
type ControlPlaneClient struct {
	baseURL    string
	httpClient *http.Client
	authToken  string
}

// NewControlPlaneClient creates a new HTTP client for the itp-v1 ControlPlane protocol.
func NewControlPlaneClient(baseURL string, httpClient *http.Client, authToken string) *ControlPlaneClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	httpClient = httputil.NewDiagnosticClient(httpClient)
	return &ControlPlaneClient{
		baseURL:    baseURL,
		httpClient: httpClient,
		authToken:  authToken,
	}
}

// RegisterClient registers a client WireGuard public key and identity with the control plane.
func (c *ControlPlaneClient) RegisterClient(ctx context.Context, publicKey string, id NodeIdentity) error {
	reqBody := registerClientRequest{
		PublicKey: publicKey,
		Identity:  id,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/auth/client", c.baseURL), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to register client key: status %d", resp.StatusCode)
	}
	return nil
}

// VerifyKey checks if a WireGuard public key is authorized and retrieves its identity.
func (c *ControlPlaneClient) VerifyKey(ctx context.Context, publicKey string) (bool, *NodeIdentity, error) {
	u := fmt.Sprintf("%s/auth/verify?key=%s", c.baseURL, url.QueryEscape(publicKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, nil, err
	}
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, nil, fmt.Errorf("verify key failed: status %d", resp.StatusCode)
	}
	var res verifyKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return false, nil, err
	}
	return res.Authorized, res.Identity, nil
}

// AuthorizedKeys returns the list of all authorized WireGuard public keys.
func (c *ControlPlaneClient) AuthorizedKeys(ctx context.Context) ([]string, error) {
	u := fmt.Sprintf("%s/auth/keys", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("authorized keys query failed: status %d", resp.StatusCode)
	}
	var keys []string
	if err := json.NewDecoder(resp.Body).Decode(&keys); err != nil {
		return nil, err
	}
	return keys, nil
}
