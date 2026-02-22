package has

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// HasRequest is the payload for notifying a service about known blocks.
type HasRequest struct {
	Addresses []string `json:"addresses"`
}

// Client implements a client for sending has requests to a has-v1 service.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new HTTP has client.
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

// Has notifies the service that a storage node holds the given blocks.
// The `storageID` is the ID of the storage node that has the blocks.
func (c *Client) Has(storageID string, addresses []string) error {
	reqBody := HasRequest{Addresses: addresses}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/has/%s", c.baseURL, storageID), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

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
