package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"invariant/internal/httputil"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// Assert that Client implements Discovery and ControlPlane interfaces
var (
	_ Discovery    = (*Client)(nil)
	_ ControlPlane = (*Client)(nil)
)

// Client implements the Discovery and ControlPlane interfaces by forwarding requests to a remote HTTP server.
type Client struct {
	baseURL    string
	httpClient *http.Client
	authToken  string
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

// WithAuthToken sets the bearer token for authenticating requests.
func (c *Client) WithAuthToken(token string) *Client {
	c.authToken = token
	return c
}

// Get retrieves the service description for the given ID.
func (c *Client) Get(ctx context.Context, id string) (ServiceDescription, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/%s", c.baseURL, id), nil)
	if err != nil {
		return ServiceDescription{}, false
	}
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
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
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
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
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
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
	return NewControlPlaneClient(c.baseURL, c.httpClient, c.authToken)
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
