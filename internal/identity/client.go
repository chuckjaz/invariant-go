package identity

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client implements the Identity interface for a remote service.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new Client for checking a remote service's Identity.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 2 * time.Second}
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	return &Client{
		baseURL:    baseURL,
		httpClient: httpClient,
	}
}

// ID retrieves the ID of the remote service. Returns an empty string on error.
func (c *Client) ID() string {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return ""
	}

	reqURL := fmt.Sprintf("%s://%s/id", u.Scheme, u.Host)
	resp, err := c.httpClient.Get(reqURL)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return ""
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	return string(body)
}

// Assert that Client implements Identity
var _ Identity = (*Client)(nil)
