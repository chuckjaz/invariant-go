package finder

import (
	"encoding/json"
	"fmt"
	"invariant/internal/has"
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
func (c *Client) Find(address string) ([]FindResponse, error) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/%s", c.baseURL, address), nil)
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
func (c *Client) Has(storageID string, addresses []string) error {
	hasClient := has.NewClient(c.baseURL, c.httpClient)
	return hasClient.Has(storageID, addresses)
}

// Notify pings the remote finder to notify it of a new finder's existence.
func (c *Client) Notify(finderID string) error {
	req, err := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/notify/%s", c.baseURL, finderID), nil)
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
