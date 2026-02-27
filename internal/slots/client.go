// Package slots provides the HTTP client for the slots service.
package slots

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Client implements the Slots interface by forwarding requests to a remote HTTP server.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new HTTP slots client.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL:    baseURL,
		httpClient: httpClient,
	}
}

// ID fetched from the remote slots service endpoint.
func (c *Client) ID() string {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/id", c.baseURL), nil)
	if err != nil {
		return ""
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}

	return string(body)
}

// Get fetches the address for the given slot ID from the remote slots service.
func (c *Client) Get(id string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/%s", c.baseURL, id), nil)
	if err != nil {
		return "", err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", ErrSlotNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// Update updates a slot on the remote slots service.
func (c *Client) Update(id string, address string, previousAddress string) error {
	updateReq := SlotUpdate{
		Address:         address,
		PreviousAddress: previousAddress,
	}
	reqData, err := json.Marshal(updateReq)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/%s", c.baseURL, id), bytes.NewReader(reqData))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrSlotNotFound
	}
	if resp.StatusCode == http.StatusConflict {
		return ErrConflict
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

// Create creates a new slot on the remote slots service.
func (c *Client) Create(id string, address string) error {
	createReq := SlotRegistration{
		Address: address,
	}
	reqData, err := json.Marshal(createReq)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/%s", c.baseURL, id), bytes.NewReader(reqData))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return ErrSlotExists
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

var _ Slots = (*Client)(nil)
