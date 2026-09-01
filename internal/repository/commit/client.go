package commit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"invariant/internal/content"
	"invariant/internal/httputil"
	"invariant/internal/identity"
)

// Assert that Client implements identity.Identity and Service
var _ identity.Identity = (*Client)(nil)
var _ Service = (*Client)(nil)

// Client implements Service over HTTP.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new remote commit.Client.
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

// ID fetched from the remote commit service endpoint.
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

// GetCommit retrieves a commit by hash over HTTP.
func (c *Client) GetCommit(ctx context.Context, commitHash string) (*Commit, error) {
	reqURL := fmt.Sprintf("%s/commit/%s", c.baseURL, commitHash)
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

	var commit Commit
	if err := json.NewDecoder(resp.Body).Decode(&commit); err != nil {
		return nil, err
	}
	return &commit, nil
}

// CreateCommit creates an immutable commit over HTTP.
func (c *Client) CreateCommit(ctx context.Context, req CreateRequest) (*Commit, string, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, "", err
	}
	reqURL := fmt.Sprintf("%s/commit", c.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("server error %d: %s", resp.StatusCode, resp.Status)
	}

	var res struct {
		Commit Commit `json:"commit"`
		Hash   string `json:"hash"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, "", err
	}
	return &res.Commit, res.Hash, nil
}

// GetHistory retrieves commit history over HTTP.
func (c *Client) GetHistory(ctx context.Context, headHash string, spineOnly bool, pathFilter string) ([]*Commit, []string, error) {
	params := url.Values{}
	params.Set("head", headHash)
	if spineOnly {
		params.Set("spine", "true")
	}
	if pathFilter != "" {
		params.Set("path", pathFilter)
	}

	reqURL := fmt.Sprintf("%s/history?%s", c.baseURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("server error %d: %s", resp.StatusCode, resp.Status)
	}

	var res struct {
		Commits []*Commit `json:"commits"`
		Hashes  []string  `json:"hashes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, nil, err
	}
	return res.Commits, res.Hashes, nil
}

// ComputeDiff calculates unified diff over HTTP.
func (c *Client) ComputeDiff(ctx context.Context, fromTree, toTree content.ContentLink) (string, DiffStat, error) {
	reqBody := struct {
		FromTree content.ContentLink `json:"fromTree"`
		ToTree   content.ContentLink `json:"toTree"`
	}{
		FromTree: fromTree,
		ToTree:   toTree,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return "", DiffStat{}, err
	}

	reqURL := fmt.Sprintf("%s/diff", c.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(data))
	if err != nil {
		return "", DiffStat{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", DiffStat{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", DiffStat{}, fmt.Errorf("server error %d: %s", resp.StatusCode, resp.Status)
	}

	var res struct {
		Diff string   `json:"diff"`
		Stat DiffStat `json:"stat"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", DiffStat{}, err
	}
	return res.Diff, res.Stat, nil
}

// SyncBranch rebases change branch over HTTP.
func (c *Client) SyncBranch(ctx context.Context, repoName, changeBranch string) (string, []string, error) {
	reqBody := struct {
		RepoName     string `json:"repoName"`
		ChangeBranch string `json:"changeBranch"`
	}{
		RepoName:     repoName,
		ChangeBranch: changeBranch,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return "", nil, err
	}

	reqURL := fmt.Sprintf("%s/sync", c.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(data))
	if err != nil {
		return "", nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("server error %d: %s", resp.StatusCode, resp.Status)
	}

	var res struct {
		NewHead   string   `json:"newHead"`
		Conflicts []string `json:"conflicts,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", nil, err
	}
	return res.NewHead, res.Conflicts, nil
}

// AbortSync restores pre-sync state over HTTP.
func (c *Client) AbortSync(ctx context.Context, repoName, changeBranch string) error {
	// AbortSync is handled at the workspace level
	return nil
}

// SubmitChange submits a change over HTTP.
func (c *Client) SubmitChange(ctx context.Context, req SubmitRequest) (*SubmitResponse, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	reqURL := fmt.Sprintf("%s/submit", c.baseURL)
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

	var res SubmitResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Blame calculates line attribution over HTTP.
func (c *Client) Blame(ctx context.Context, commitHash, filePath string) ([]BlameLine, error) {
	params := url.Values{}
	params.Set("commit", commitHash)
	params.Set("file", filePath)

	reqURL := fmt.Sprintf("%s/blame?%s", c.baseURL, params.Encode())
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

	var lines []BlameLine
	if err := json.NewDecoder(resp.Body).Decode(&lines); err != nil {
		return nil, err
	}
	return lines, nil
}

// Bisect calculates candidate midpoint commit over HTTP.
func (c *Client) Bisect(ctx context.Context, goodCommits, badCommits []string) (string, int, error) {
	reqBody := struct {
		Good []string `json:"good"`
		Bad  []string `json:"bad"`
	}{
		Good: goodCommits,
		Bad:  badCommits,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return "", 0, err
	}

	reqURL := fmt.Sprintf("%s/bisect", c.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(data))
	if err != nil {
		return "", 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("server error %d: %s", resp.StatusCode, resp.Status)
	}

	var res struct {
		Candidate string `json:"candidate"`
		Remaining int    `json:"remaining"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", 0, err
	}
	return res.Candidate, res.Remaining, nil
}

// InteractiveRebase applies rebase plan over HTTP.
func (c *Client) InteractiveRebase(ctx context.Context, repoName, changeBranch, baseCommit string, plan []RebaseAction) (string, error) {
	reqBody := struct {
		RepoName     string         `json:"repoName"`
		ChangeBranch string         `json:"changeBranch"`
		BaseCommit   string         `json:"baseCommit"`
		Plan         []RebaseAction `json:"plan"`
	}{
		RepoName:     repoName,
		ChangeBranch: changeBranch,
		BaseCommit:   baseCommit,
		Plan:         plan,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	reqURL := fmt.Sprintf("%s/rebase", c.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("server error %d: %s", resp.StatusCode, resp.Status)
	}

	var res struct {
		NewHead string `json:"newHead"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	return res.NewHead, nil
}
