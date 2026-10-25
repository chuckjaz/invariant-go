package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/chuckjaz/invariant-go/internal/httputil"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Assert that Client implements Discovery, ControlPlane, ServiceKeyManager, and InvitationManager interfaces
var (
	_ Discovery         = (*Client)(nil)
	_ ControlPlane      = (*Client)(nil)
	_ ServiceKeyManager = (*Client)(nil)
	_ InvitationManager = (*Client)(nil)
)

// Client implements the Discovery and ControlPlane interfaces by forwarding requests to a remote HTTP server.
type Client struct {
	baseURL    string
	httpClient *http.Client
	token      string
}

// NewClient creates a new HTTP discovery client.
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

// WithServiceKey sets the service key for authenticating requests.
func (c *Client) WithServiceKey(key string) *Client {
	c.token = key
	return c
}

// WithUserToken sets the OAuth user token for authenticating requests.
func (c *Client) WithUserToken(token string) *Client {
	c.token = token
	return c
}

// WithAccessToken sets the access token for authenticating requests.
func (c *Client) WithAccessToken(token string) *Client {
	c.token = token
	return c
}

// Token returns the configured bearer token on this client.
func (c *Client) Token() string {
	return c.token
}

// Get retrieves the service description for the given ID.
func (c *Client) Get(ctx context.Context, id string) (ServiceDescription, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/%s", c.baseURL, id), nil)
	if err != nil {
		return ServiceDescription{}, false
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
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

// Find searches for services by protocol and/or tag up to a certain count.
func (c *Client) Find(ctx context.Context, protocol, tag string, count int) ([]ServiceDescription, error) {
	u, err := url.Parse(fmt.Sprintf("%s/", c.baseURL))
	if err != nil {
		return nil, err
	}

	q := u.Query()
	if protocol != "" {
		q.Set("protocol", protocol)
	}
	if tag != "" {
		q.Set("tag", tag)
	}
	q.Set("count", strconv.Itoa(count))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
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
func (c *Client) Register(ctx context.Context, reg ServiceRegistration) error {
	data, err := json.Marshal(reg)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("%s/%s", c.baseURL, reg.ID), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

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

func (c *Client) cpClient() *ControlPlaneClient {
	return NewControlPlaneClient(c.baseURL, c.httpClient, c.token)
}

// RegisterClient registers a client WireGuard public key and identity with the control plane.
func (c *Client) RegisterClient(ctx context.Context, publicKey string, id NodeIdentity) error {
	return c.cpClient().RegisterClient(ctx, publicKey, id)
}

// VerifyKey checks if a WireGuard public key is authorized and retrieves its identity.
func (c *Client) VerifyKey(ctx context.Context, publicKey string) (bool, *NodeIdentity, error) {
	return c.cpClient().VerifyKey(ctx, publicKey)
}

// AuthorizedKeys returns the list of all authorized WireGuard public keys.
func (c *Client) AuthorizedKeys(ctx context.Context) ([]string, error) {
	return c.cpClient().AuthorizedKeys(ctx)
}

// CreateServiceKey creates a new service key on Discovery.
func (c *Client) CreateServiceKey(ctx context.Context, name string, creator NodeIdentity, expiresAt *time.Time) (*ServiceKeyRecord, error) {
	reqBody := map[string]string{
		"name": name,
	}
	if expiresAt != nil {
		reqBody["expires_in"] = time.Until(*expiresAt).String()
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/auth/service-key", c.baseURL), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var rec ServiceKeyRecord
	if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// VerifyServiceKey checks if a service key is valid.
func (c *Client) VerifyServiceKey(ctx context.Context, key string) (bool, *ServiceKeyRecord, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/auth/verify-service-key?key=%s", c.baseURL, url.QueryEscape(key)), nil)
	if err != nil {
		return false, nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var vResp verifyServiceKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&vResp); err != nil {
		return false, nil, err
	}
	return vResp.Valid, vResp.Record, nil
}

// ListServiceKeys lists active service keys.
func (c *Client) ListServiceKeys(ctx context.Context) ([]ServiceKeyRecord, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/auth/service-keys", c.baseURL), nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var recs []ServiceKeyRecord
	if err := json.NewDecoder(resp.Body).Decode(&recs); err != nil {
		return nil, err
	}
	return recs, nil
}

// RevokeServiceKey revokes a service key by key or ID.
func (c *Client) RevokeServiceKey(ctx context.Context, keyOrID string, revoker NodeIdentity) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/auth/service-key/%s", c.baseURL, url.PathEscape(keyOrID)), nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// CreateInvitation creates a new invitation code on Discovery.
func (c *Client) CreateInvitation(ctx context.Context, name string, role TokenRole, creator NodeIdentity, ttl time.Duration, email ...string) (*InvitationRecord, error) {
	reqBody := createInvitationRequest{
		Name: name,
		Role: role,
	}
	if len(email) > 0 {
		reqBody.Email = strings.TrimSpace(email[0])
	}
	if ttl > 0 {
		reqBody.TTL = ttl.String()
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/auth/invite", c.baseURL), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var inv InvitationRecord
	if err := json.NewDecoder(resp.Body).Decode(&inv); err != nil {
		return nil, err
	}
	return &inv, nil
}

// RedeemInvitation exchanges an invitation code and WireGuard public key for an access token.
func (c *Client) RedeemInvitation(ctx context.Context, code string, publicKey string, identity NodeIdentity) (*AccessTokenRecord, error) {
	baseURL := c.baseURL
	if baseURL == "" {
		if _, cpURL := ParseInvitationCode(code); cpURL != "" {
			baseURL = cpURL
		}
	}

	reqBody := redeemInvitationRequest{
		Code:      code,
		PublicKey: publicKey,
		Identity:  identity,
	}
	if c.token != "" {
		reqBody.OAuthToken = c.token
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/auth/invite/redeem", baseURL), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var tokRec AccessTokenRecord
	if err := json.NewDecoder(resp.Body).Decode(&tokRec); err != nil {
		return nil, err
	}
	return &tokRec, nil
}

// ListInvitations lists invitations on Discovery.
func (c *Client) ListInvitations(ctx context.Context) ([]InvitationRecord, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/auth/invites", c.baseURL), nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var recs []InvitationRecord
	if err := json.NewDecoder(resp.Body).Decode(&recs); err != nil {
		return nil, err
	}
	return recs, nil
}

// RevokeInvitation revokes an invitation by code or ID.
func (c *Client) RevokeInvitation(ctx context.Context, codeOrID string, revoker NodeIdentity) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/auth/invite/%s", c.baseURL, url.PathEscape(codeOrID)), nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// VerifyAccessToken verifies an access token via Discovery.
func (c *Client) VerifyAccessToken(ctx context.Context, token string) (bool, *AccessTokenRecord, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/auth/verify-token?token=%s", c.baseURL, url.QueryEscape(token)), nil)
	if err != nil {
		return false, nil, err
	}
	bearer := c.token
	if bearer == "" {
		bearer = token
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, nil, ErrAccessTokenNotFound
	}

	var vResp verifyAccessTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&vResp); err != nil {
		return false, nil, err
	}
	if !vResp.Valid {
		return false, nil, ErrAccessTokenNotFound
	}
	return true, vResp.Record, nil
}

// ListAccessTokens lists issued access tokens on Discovery.
func (c *Client) ListAccessTokens(ctx context.Context) ([]AccessTokenRecord, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/auth/tokens", c.baseURL), nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var recs []AccessTokenRecord
	if err := json.NewDecoder(resp.Body).Decode(&recs); err != nil {
		return nil, err
	}
	return recs, nil
}

// RevokeAccessToken revokes an access token by token or ID.
func (c *Client) RevokeAccessToken(ctx context.Context, tokenOrID string, revoker NodeIdentity) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/auth/token/%s", c.baseURL, url.PathEscape(tokenOrID)), nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
