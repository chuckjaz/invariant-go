package names

import (
	"context"
	"encoding/json"
	"fmt"
	"invariant/internal/httputil"
	"net/http"
	"net/url"
	"strings"
)

// Client implements the Names interface by forwarding requests to a remote HTTP server.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new HTTP names client.
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

// Get retrieves the name entry for a given name.
func (c *Client) Get(ctx context.Context, name string) (NameEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/%s", c.baseURL, name), nil)
	if err != nil {
		return NameEntry{}, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return NameEntry{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return NameEntry{}, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return NameEntry{}, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var entry NameEntry
	if err := json.NewDecoder(resp.Body).Decode(&entry); err != nil {
		return NameEntry{}, err
	}

	return entry, nil
}

// Put updates or creates a name entry.
func (c *Client) Put(ctx context.Context, name string, value string, tokens []string) error {
	u, err := url.Parse(fmt.Sprintf("%s/%s", c.baseURL, name))
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("value", value)
	if len(tokens) > 0 {
		q.Set("tokens", strings.Join(tokens, ","))
	} else {
		q.Set("tokens", "") // Just in case
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), nil)
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

// Delete removes a name entry.
func (c *Client) Delete(ctx context.Context, name string, expectedValue string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/%s", c.baseURL, name), nil)
	if err != nil {
		return err
	}

	if expectedValue != "" {
		req.Header.Set("If-Match", expectedValue)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode == http.StatusPreconditionFailed {
		return ErrPreconditionFailed
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

// Lookup queries the service for aliases registered against an ID.
func (c *Client) Lookup(ctx context.Context, id string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/lookup/%s", c.baseURL, id), nil)
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

	var names []string
	if err := json.NewDecoder(resp.Body).Decode(&names); err != nil {
		return nil, err
	}

	return names, nil
}

// Assert that Client implements the Names interface
var _ Names = (*Client)(nil)
