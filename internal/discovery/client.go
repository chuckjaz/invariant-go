package discovery

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// Client implements the Discovery interface by forwarding requests to a remote HTTP server.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new HTTP discovery client.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL:    baseURL,
		httpClient: httpClient,
	}
}

// Get retrieves the service description for the given ID.
func (c *Client) Get(id string) (ServiceDescription, bool) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/discovery/%s", c.baseURL, id), nil)
	if err != nil {
		return ServiceDescription{}, false
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

// Find searches for services by protocol up to a certain count.
func (c *Client) Find(protocol string, count int) ([]ServiceDescription, error) {
	u, err := url.Parse(fmt.Sprintf("%s/discovery", c.baseURL))
	if err != nil {
		return nil, err
	}

	q := u.Query()
	q.Set("protocol", protocol)
	q.Set("count", strconv.Itoa(count))
	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
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

	var descs []ServiceDescription
	if err := json.NewDecoder(resp.Body).Decode(&descs); err != nil {
		return nil, err
	}

	return descs, nil
}

// Register registers a new service.
func (c *Client) Register(reg ServiceRegistration) error {
	data, err := json.Marshal(reg)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/discovery", c.baseURL), bytes.NewReader(data))
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
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	return nil
}

// Assert that Client implements the Discovery interface
var _ Discovery = (*Client)(nil)
