package kv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
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

// StartTransaction starts a new transaction.
func (c *Client) StartTransaction(ctx context.Context, sequential bool) (uint64, error) {
	reqURL := fmt.Sprintf("%s/tx/start?sequential=%t", c.baseURL, sequential)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, nil)
	if err != nil {
		return 0, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	var result map[string]uint64
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}
	return result["transaction_id"], nil
}

// CommitTransaction commits an active transaction.
func (c *Client) CommitTransaction(ctx context.Context, txID uint64) error {
	reqURL := fmt.Sprintf("%s/tx/commit?tx=%d", c.baseURL, txID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, nil)
	if err != nil {
		return err
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

// AbortTransaction aborts an active transaction.
func (c *Client) AbortTransaction(ctx context.Context, txID uint64) error {
	reqURL := fmt.Sprintf("%s/tx/abort?tx=%d", c.baseURL, txID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, nil)
	if err != nil {
		return err
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

// CreateCheckpoint creates a read-only checkpoint transaction.
func (c *Client) CreateCheckpoint(ctx context.Context) (uint64, error) {
	reqURL := fmt.Sprintf("%s/tx/checkpoint", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, nil)
	if err != nil {
		return 0, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	var result map[string]uint64
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}
	return result["transaction_id"], nil
}

// Put adds a new key-value pair to the remote kv service.
func (c *Client) Put(ctx context.Context, txID *uint64, key string, value []byte) (uint64, error) {
	reqURL := fmt.Sprintf("%s/put?key=%s", c.baseURL, url.QueryEscape(key))
	if txID != nil {
		reqURL += fmt.Sprintf("&tx=%d", *txID)
	}
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
func (c *Client) Get(ctx context.Context, txID *uint64, key string) ([]byte, uint64, error) {
	reqURL := fmt.Sprintf("%s/get?key=%s", c.baseURL, url.QueryEscape(key))
	if txID != nil {
		reqURL += fmt.Sprintf("&tx=%d", *txID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, 0, fmt.Errorf("key not found: %s", key)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	seqStr := resp.Header.Get("X-Transaction-ID")
	var seq uint64
	if seqStr != "" {
		seq, err = strconv.ParseUint(seqStr, 10, 64)
		if err != nil {
			return nil, 0, fmt.Errorf("invalid transaction ID: %s", seqStr)
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}

	return body, seq, nil
}

// BatchPut adds multiple key-value pairs to the remote kv service.
func (c *Client) BatchPut(ctx context.Context, txID *uint64, kvs map[string][]byte) (uint64, error) {
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)

	go func() {
		var err error
		for key, val := range kvs {
			h := make(textproto.MIMEHeader)
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"`, key))
			part, errPart := writer.CreatePart(h)
			if errPart != nil {
				err = errPart
				break
			}
			if _, errCopy := part.Write(val); errCopy != nil {
				err = errCopy
				break
			}
		}
		if err == nil {
			err = writer.Close()
		}
		pw.CloseWithError(err)
	}()

	reqURL := fmt.Sprintf("%s/batch_put", c.baseURL)
	if txID != nil {
		reqURL += fmt.Sprintf("?tx=%d", *txID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, pr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	seqStr := resp.Header.Get("X-Transaction-ID")
	if seqStr == "" {
		return 0, fmt.Errorf("missing X-Transaction-ID header")
	}

	seq, err := strconv.ParseUint(seqStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid transaction ID: %s", seqStr)
	}

	return seq, nil
}

// BatchGet fetches the values for the given keys from the remote kv service.
func (c *Client) BatchGet(ctx context.Context, txID *uint64, keys []string) (map[string]ValueWithTransaction, error) {
	data, err := json.Marshal(keys)
	if err != nil {
		return nil, err
	}

	reqURL := fmt.Sprintf("%s/batch_get", c.baseURL)
	if txID != nil {
		reqURL += fmt.Sprintf("?tx=%d", *txID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	_, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	boundary := params["boundary"]

	reader := multipart.NewReader(resp.Body, boundary)
	results := make(map[string]ValueWithTransaction)

	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		key := part.FormName()
		if key == "" {
			continue
		}

		seqStr := part.Header.Get("X-Transaction-ID")
		var seq uint64
		if seqStr != "" {
			seq, _ = strconv.ParseUint(seqStr, 10, 64)
		}

		val, err := io.ReadAll(part)
		if err != nil {
			return nil, err
		}

		results[key] = ValueWithTransaction{
			Value:         val,
			TransactionID: seq,
		}
	}

	return results, nil
}

// GetHistory fetches historical values for the given key from the remote kv service.
func (c *Client) GetHistory(ctx context.Context, txID *uint64, key string, minTxID uint64, maxTxID uint64, pageSize int) (HistoryPage, error) {
	reqURL := fmt.Sprintf("%s/history?key=%s&min=%d&max=%d&limit=%d", c.baseURL, url.QueryEscape(key), minTxID, maxTxID, pageSize)
	if txID != nil {
		reqURL += fmt.Sprintf("&tx=%d", *txID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return HistoryPage{}, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return HistoryPage{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return HistoryPage{}, fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	var page HistoryPage
	page.HasMore = resp.Header.Get("X-Has-More") == "true"

	_, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return HistoryPage{}, err
	}
	boundary := params["boundary"]

	reader := multipart.NewReader(resp.Body, boundary)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return HistoryPage{}, err
		}

		seqStr := part.Header.Get("X-Transaction-ID")
		var seq uint64
		if seqStr != "" {
			seq, _ = strconv.ParseUint(seqStr, 10, 64)
		}

		val, err := io.ReadAll(part)
		if err != nil {
			return HistoryPage{}, err
		}

		page.Values = append(page.Values, ValueWithTransaction{
			Value:         val,
			TransactionID: seq,
		})
	}

	return page, nil
}

// BatchGetHistory fetches historical values for the given keys from the remote kv service.
func (c *Client) BatchGetHistory(ctx context.Context, txID *uint64, keys []string, minTxID uint64, maxTxID uint64, pageSize int) (map[string]HistoryPage, error) {
	data, err := json.Marshal(keys)
	if err != nil {
		return nil, err
	}

	reqURL := fmt.Sprintf("%s/batch_history?min=%d&max=%d&limit=%d", c.baseURL, minTxID, maxTxID, pageSize)
	if txID != nil {
		reqURL += fmt.Sprintf("&tx=%d", *txID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	_, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	boundary := params["boundary"]

	reader := multipart.NewReader(resp.Body, boundary)
	results := make(map[string]HistoryPage)

	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		key := part.FormName()
		if key == "" {
			continue
		}

		seqStr := part.Header.Get("X-Transaction-ID")
		var seq uint64
		if seqStr != "" {
			seq, _ = strconv.ParseUint(seqStr, 10, 64)
		}

		hasMoreStr := part.Header.Get("X-Has-More")

		val, err := io.ReadAll(part)
		if err != nil {
			return nil, err
		}

		page := results[key]
		if hasMoreStr == "true" {
			page.HasMore = true
		}
		page.Values = append(page.Values, ValueWithTransaction{
			Value:         val,
			TransactionID: seq,
		})
		results[key] = page
	}

	return results, nil
}
