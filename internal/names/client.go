package names

import (
	"encoding/json"
	"fmt"
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
	return &Client{
		baseURL:    baseURL,
		httpClient: httpClient,
	}
}

// Get retrieves the name entry for a given name.
func (c *Client) Get(name string) (NameEntry, error) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/%s", c.baseURL, name), nil)
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
func (c *Client) Put(name string, value string, tokens []string) error {
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

	req, err := http.NewRequest(http.MethodPut, u.String(), nil)
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
func (c *Client) Delete(name string, expectedValue string) error {
	req, err := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/%s", c.baseURL, name), nil)
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

// Assert that Client implements the Names interface
var _ Names = (*Client)(nil)
