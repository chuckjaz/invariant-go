// Package slots provides the HTTP client for the slots service.
package slots

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"invariant/internal/httputil"
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
	httpClient = httputil.NewDiagnosticClient(httpClient)
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
func (c *Client) Get(ctx context.Context, id string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/%s", c.baseURL, id), nil)
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
// The auth parameter accepts an Ed25519 private key (64 bytes) to sign the update if the slot is protected.
func (c *Client) Update(ctx context.Context, id string, address string, previousAddress string, auth []byte) error {
	updateReq := SlotUpdate{
		Address:         address,
		PreviousAddress: previousAddress,
	}
	reqData, err := json.Marshal(updateReq)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("%s/%s", c.baseURL, id), bytes.NewReader(reqData))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	if len(auth) == ed25519.PrivateKeySize {
		signature := ed25519.Sign(auth, reqData)
		req.Header.Set("Authorization", hex.EncodeToString(signature))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrSlotNotFound
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
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
func (c *Client) Create(ctx context.Context, id string, address string, policy string) error {
	createReq := SlotRegistration{
		Address: address,
	}
	reqData, err := json.Marshal(createReq)
	if err != nil {
		return err
	}

	u := fmt.Sprintf("%s/%s", c.baseURL, id)
	if policy != "" {
		u = fmt.Sprintf("%s?protected=%s", u, policy)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(reqData))
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

// List is not supported on the client side at this time.
func (c *Client) List(ctx context.Context, chunkSize int) <-chan []string {
	ch := make(chan []string)
	close(ch)
	return ch
}

// Subscribe is not supported on the client side at this time.
func (c *Client) Subscribe(ctx context.Context) <-chan string {
	ch := make(chan string)
	close(ch)
	return ch
}

var _ Slots = (*Client)(nil)
