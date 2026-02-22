package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// Client implements the Storage interface by forwarding requests to a remote HTTP server.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new HTTP storage client.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL:    baseURL,
		httpClient: httpClient,
	}
}

// Has checks if the storage contains the given address.
func (c *Client) Has(address string) bool {
	req, err := http.NewRequest(http.MethodHead, fmt.Sprintf("%s/storage/%s", c.baseURL, address), nil)
	if err != nil {
		return false
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// Get retrieves the data for the given address.
func (c *Client) Get(address string) (io.ReadCloser, bool) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/storage/%s", c.baseURL, address), nil)
	if err != nil {
		return nil, false
	}

	resp, err := c.httpClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return nil, false
	}

	return resp.Body, true
}

// Store saves data and returns its content-based address.
func (c *Client) Store(r io.Reader) (string, error) {
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/storage/", c.baseURL), r)
	if err != nil {
		return "", err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// StoreAt saves data at the specified address.
func (c *Client) StoreAt(address string, r io.Reader) (bool, error) {
	req, err := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/storage/%s", c.baseURL, address), r)
	if err != nil {
		return false, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, nil
	}

	return true, nil
}

// Size returns the size of the data at the given address.
func (c *Client) Size(address string) (int64, bool) {
	req, err := http.NewRequest(http.MethodHead, fmt.Sprintf("%s/storage/%s", c.baseURL, address), nil)
	if err != nil {
		return 0, false
	}

	resp, err := c.httpClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return 0, false
	}
	defer resp.Body.Close()

	sizeStr := resp.Header.Get("Content-Length")
	if sizeStr == "" {
		return 0, false
	}

	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		return 0, false
	}

	return size, true
}

// Fetch instructs the remote server to fetch data from another container.
func (c *Client) Fetch(address, container string) error {
	reqBody := StorageFetchRequest{
		Address:   address,
		Container: container,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/storage/fetch", c.baseURL), bytes.NewReader(data))
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

// Assert that Client implements the Storage interface
var _ Storage = (*Client)(nil)
