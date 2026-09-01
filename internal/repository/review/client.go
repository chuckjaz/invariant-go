package review

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"invariant/internal/httputil"
	"invariant/internal/identity"
	repoid "invariant/internal/repository/identity"
)

// Assert that Client implements identity.Identity and Service
var _ identity.Identity = (*Client)(nil)
var _ Service = (*Client)(nil)

// Client implements Service over HTTP.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new remote review.Client.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	httpClient = httputil.NewDiagnosticClient(httpClient)
	return &Client{
		baseURL:    strings.TrimSuffix(baseURL, "/"),
		httpClient: httpClient,
	}
}

// ID fetched from the remote review service endpoint.
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
	return strings.TrimSpace(string(body))
}

// RequestReview requests a code review over HTTP.
func (c *Client) RequestReview(ctx context.Context, repoName, branchName string, author repoid.Identity) (*Record, error) {
	reqBody := struct {
		RepoName   string          `json:"repoName"`
		BranchName string          `json:"branchName"`
		Author     repoid.Identity `json:"author"`
	}{
		RepoName:   repoName,
		BranchName: branchName,
		Author:     author,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	reqURL := fmt.Sprintf("%s/reviews/request", c.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server error %d: %s", resp.StatusCode, resp.Status)
	}

	var rec Record
	if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// GetReview retrieves a code review over HTTP.
func (c *Client) GetReview(ctx context.Context, identifier string) (*Record, error) {
	reqURL := fmt.Sprintf("%s/reviews/%s", c.baseURL, identifier)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server error %d: %s", resp.StatusCode, resp.Status)
	}

	var rec Record
	if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// StartReview marks review in progress over HTTP.
func (c *Client) StartReview(ctx context.Context, token string, reviewer repoid.Identity) error {
	reqBody := struct {
		Reviewer repoid.Identity `json:"reviewer"`
	}{
		Reviewer: reviewer,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	reqURL := fmt.Sprintf("%s/reviews/%s/start", c.baseURL, token)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server error %d: %s", resp.StatusCode, resp.Status)
	}
	return nil
}

// AddComments adds review comments over HTTP.
func (c *Client) AddComments(ctx context.Context, token string, comments []ReviewComment, author repoid.Identity) error {
	reqBody := struct {
		Comments []ReviewComment `json:"comments"`
		Author   repoid.Identity `json:"author"`
	}{
		Comments: comments,
		Author:   author,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	reqURL := fmt.Sprintf("%s/reviews/%s/comments", c.baseURL, token)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server error %d: %s", resp.StatusCode, resp.Status)
	}
	return nil
}

// ApproveReview approves review over HTTP.
func (c *Client) ApproveReview(ctx context.Context, token string, reviewer repoid.Identity) error {
	reqBody := struct {
		Reviewer repoid.Identity `json:"reviewer"`
	}{
		Reviewer: reviewer,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	reqURL := fmt.Sprintf("%s/reviews/%s/approve", c.baseURL, token)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server error %d: %s", resp.StatusCode, resp.Status)
	}
	return nil
}

// RejectReview rejects review over HTTP.
func (c *Client) RejectReview(ctx context.Context, token string, reviewer repoid.Identity) error {
	reqBody := struct {
		Reviewer repoid.Identity `json:"reviewer"`
	}{
		Reviewer: reviewer,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	reqURL := fmt.Sprintf("%s/reviews/%s/reject", c.baseURL, token)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server error %d: %s", resp.StatusCode, resp.Status)
	}
	return nil
}

// AbandonReview abandons review over HTTP.
func (c *Client) AbandonReview(ctx context.Context, token string, author repoid.Identity) error {
	reqBody := struct {
		Author repoid.Identity `json:"author"`
	}{
		Author: author,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	reqURL := fmt.Sprintf("%s/reviews/%s/abandon", c.baseURL, token)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server error %d: %s", resp.StatusCode, resp.Status)
	}
	return nil
}
