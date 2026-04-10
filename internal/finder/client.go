package finder

import (
	"context"
	"encoding/json"
	"fmt"
	"invariant/internal/httputil"
	"invariant/internal/notify"
	"net/http"
)

// Client implements the Finder interface by forwarding requests to a remote HTTP server.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new HTTP finder client.
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

// ID is not implemented on the client side since it usually returns the local ID.
func (c *Client) ID() string {
	return ""
}

// Find looks up a block address.
func (c *Client) Find(ctx context.Context, address string) ([]FindResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/%s", c.baseURL, address), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var responses []FindResponse
	if err := json.NewDecoder(resp.Body).Decode(&responses); err != nil {
		return nil, err
	}

	return responses, nil
}

// Has notifies the finder service that a storage node holds the given blocks.
func (c *Client) Notify(ctx context.Context, storageID string, addresses []string) error {
	hasClient := notify.NewClient(c.baseURL, c.httpClient)
	return hasClient.Notify(storageID, addresses)
}

// Peer pings the remote finder to notify it of a new finder's existence.
func (c *Client) Peer(ctx context.Context, finderID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("%s/peer/%s", c.baseURL, finderID), nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

var _ Finder = (*Client)(nil)
