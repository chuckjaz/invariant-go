package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/chuckjaz/invariant-go/internal/httputil"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Assert that Client implements Discovery, ControlPlane, and ServiceKeyManager interfaces
var (
	_ Discovery         = (*Client)(nil)
	_ ControlPlane      = (*Client)(nil)
	_ ServiceKeyManager = (*Client)(nil)
)

// Client implements the Discovery and ControlPlane interfaces by forwarding requests to a remote HTTP server.
type Client struct {
	baseURL    string
	httpClient *http.Client
	token      string
}

// NewClient creates a new HTTP discovery client.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	httpClient = httputil.NewDiagnosticClient(httpClient)
	return &Client{
		baseURL:    baseURL,
		httpClient: httpClient,
	}
}

// WithServiceKey sets the service key for authenticating requests.
func (c *Client) WithServiceKey(key string) *Client {
	c.token = key
	return c
}

// WithUserToken sets the OAuth user token for authenticating requests.
func (c *Client) WithUserToken(token string) *Client {
	c.token = token
	return c
}

// Get retrieves the service description for the given ID.
func (c *Client) Get(ctx context.Context, id string) (ServiceDescription, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/%s", c.baseURL, id), nil)
	if err != nil {
		return ServiceDescription{}, false
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return ServiceDescription{}, false
	}
	defer resp.Body.Close()

	var desc ServiceDescription
	if err := json.NewDecoder(resp.Body).Decode(&desc); err != nil {
		return ServiceDescription{}, false
	}

	return desc, true
}

// Find searches for services by protocol and/or tag up to a certain count.
func (c *Client) Find(ctx context.Context, protocol, tag string, count int) ([]ServiceDescription, error) {
	u, err := url.Parse(fmt.Sprintf("%s/", c.baseURL))
	if err != nil {
		return nil, err
	}

	q := u.Query()
	if protocol != "" {
		q.Set("protocol", protocol)
	}
	if tag != "" {
		q.Set("tag", tag)
	}
	q.Set("count", strconv.Itoa(count))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var descs []ServiceDescription
	if err := json.NewDecoder(resp.Body).Decode(&descs); err != nil {
		return nil, err
	}

	return descs, nil
}

// Register registers a new service.
func (c *Client) Register(ctx context.Context, reg ServiceRegistration) error {
	data, err := json.Marshal(reg)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("%s/%s", c.baseURL, reg.ID), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	return nil
}

func (c *Client) cpClient() *ControlPlaneClient {
	return NewControlPlaneClient(c.baseURL, c.httpClient, c.token)
}

// RegisterClient registers a client WireGuard public key and identity with the control plane.
func (c *Client) RegisterClient(ctx context.Context, publicKey string, id NodeIdentity) error {
	return c.cpClient().RegisterClient(ctx, publicKey, id)
}

// VerifyKey checks if a WireGuard public key is authorized and retrieves its identity.
func (c *Client) VerifyKey(ctx context.Context, publicKey string) (bool, *NodeIdentity, error) {
	return c.cpClient().VerifyKey(ctx, publicKey)
}

// AuthorizedKeys returns the list of all authorized WireGuard public keys.
func (c *Client) AuthorizedKeys(ctx context.Context) ([]string, error) {
	return c.cpClient().AuthorizedKeys(ctx)
}

// CreateServiceKey creates a new service key on Discovery.
func (c *Client) CreateServiceKey(ctx context.Context, name string, creator NodeIdentity, expiresAt *time.Time) (*ServiceKeyRecord, error) {
	reqBody := map[string]string{
		"name": name,
	}
	if expiresAt != nil {
		reqBody["expires_in"] = time.Until(*expiresAt).String()
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/auth/service-key", c.baseURL), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var rec ServiceKeyRecord
	if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// VerifyServiceKey checks if a service key is valid.
func (c *Client) VerifyServiceKey(ctx context.Context, key string) (bool, *ServiceKeyRecord, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/auth/verify-service-key?key=%s", c.baseURL, url.QueryEscape(key)), nil)
	if err != nil {
		return false, nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var vResp verifyServiceKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&vResp); err != nil {
		return false, nil, err
	}
	return vResp.Valid, vResp.Record, nil
}

// ListServiceKeys lists active service keys.
func (c *Client) ListServiceKeys(ctx context.Context) ([]ServiceKeyRecord, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/auth/service-keys", c.baseURL), nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var recs []ServiceKeyRecord
	if err := json.NewDecoder(resp.Body).Decode(&recs); err != nil {
		return nil, err
	}
	return recs, nil
}

// RevokeServiceKey revokes a service key by key or ID.
func (c *Client) RevokeServiceKey(ctx context.Context, keyOrID string, revoker NodeIdentity) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/auth/service-key/%s", c.baseURL, url.PathEscape(keyOrID)), nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
