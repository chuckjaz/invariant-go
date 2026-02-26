package distribute

import (
	"fmt"
	"net/http"
)

// Client implements a client for interacting with a remote distribute service.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new HTTP distribute client.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	// baseURL should not have a trailing slash
	return &Client{
		baseURL:    baseURL,
		httpClient: httpClient,
	}
}

// Register registers a storage node ID with the distribute service.
func (c *Client) Register(id string) error {
	req, err := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/register/%s", c.baseURL, id), nil)
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
