package kv

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"invariant/internal/httputil"
)

// Client implements a client for the kv service.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new HTTP kv client.
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

// Put adds a new key-value pair to the remote kv service.
func (c *Client) Put(ctx context.Context, key string, value []byte) (uint64, error) {
	reqURL := fmt.Sprintf("%s/put?key=%s", c.baseURL, url.QueryEscape(key))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(value))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	seqStr := resp.Header.Get("X-Sequence")
	if seqStr == "" {
		return 0, fmt.Errorf("missing X-Sequence header")
	}

	seq, err := strconv.ParseUint(seqStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid sequence number: %s", seqStr)
	}

	return seq, nil
}

// Get fetches the value for the given key from the remote kv service.
func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	reqURL := fmt.Sprintf("%s/get?key=%s", c.baseURL, url.QueryEscape(key))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("key not found: %s", key)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return body, nil
}
