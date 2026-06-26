package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"

	"invariant/internal/config"
	"invariant/internal/discovery"
	"invariant/internal/kv"
)

type GitHubCommit struct {
	SHA  string `json:"sha"`
	Tree struct {
		SHA string `json:"sha"`
	} `json:"tree"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
}

type GitHubTreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

type GitHubTree struct {
	SHA  string            `json:"sha"`
	Tree []GitHubTreeEntry `json:"tree"`
}

func runScanRepo(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("scan-repo", flag.ExitOnError)
	var owner string
	var repo string
	var token string
	var commit string
	var depth int
	var kvURL string
	var discoveryURL string
	var concurrency int

	fs.StringVar(&owner, "owner", "", "GitHub owner/org name (required)")
	fs.StringVar(&repo, "repo", "", "GitHub repository name (required)")
	fs.StringVar(&token, "token", "", "GitHub personal access token (optional)")
	fs.StringVar(&commit, "commit", "", "Git commit SHA1 to start scanning from (required)")
	fs.IntVar(&depth, "depth", 1, "Ancestery depth limit for scanning commits (use -1 for unlimited)")
	fs.StringVar(&kvURL, "kv", "", "URL of the KV service")
	fs.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	fs.IntVar(&concurrency, "concurrency", 20, "Number of concurrent requests to GitHub")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: invariant scan-repo --owner <owner> --repo <repo> --commit <commit-sha> [options]\n")
		fmt.Fprintf(os.Stderr, "Scans the GitHub repository starting from a commit SHA1 and indexes Git SHA1 to SHA256 blob mappings in the KV service.\n\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if owner == "" || repo == "" || commit == "" {
		fmt.Fprintf(os.Stderr, "Error: --owner, --repo, and --commit are required parameters.\n\n")
		fs.Usage()
		os.Exit(1)
	}

	ctx := context.Background()

	// 1. Resolve KV service URL
	if kvURL == "" {
		if discoveryURL == "" && globalCfg != nil {
			discoveryURL = globalCfg.Discovery
		}
		if discoveryURL == "" {
			fmt.Fprintf(os.Stderr, "Error: Discovery service URL not configured. Provide via --discovery or configuration file.\n")
			os.Exit(1)
		}

		dClient := discovery.NewClient(discoveryURL, nil)
		svcs, err := dClient.Find(ctx, "kv-v1", 1)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error querying discovery service: %v\n", err)
			os.Exit(1)
		}
		if len(svcs) == 0 {
			fmt.Fprintf(os.Stderr, "Error: no kv-v1 service found in discovery\n")
			os.Exit(1)
		}
		kvURL = svcs[0].Address
	}

	kvClient := kv.NewClient(kvURL, nil)
	httpClient := http.DefaultClient

	// 2. Commit Traversal BFS/DFS
	fmt.Printf("Traversing commit history starting from commit %s (depth limit: %d)...\n", commit, depth)

	type QueueItem struct {
		sha   string
		depth int
	}

	queue := []QueueItem{{sha: commit, depth: 1}}
	visited := make(map[string]bool)
	var rootTrees []string

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		if visited[item.sha] {
			continue
		}
		visited[item.sha] = true

		// Fetch commit JSON
		commitURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/commits/%s", owner, repo, item.sha)
		resp, err := sendGitHubRequest(ctx, httpClient, token, "GET", commitURL, "application/vnd.github.v3+json")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching commit %s: %v\n", item.sha, err)
			os.Exit(1)
		}

		var ghCommit GitHubCommit
		if err := json.NewDecoder(resp.Body).Decode(&ghCommit); err != nil {
			resp.Body.Close()
			fmt.Fprintf(os.Stderr, "Error decoding commit JSON for %s: %v\n", item.sha, err)
			os.Exit(1)
		}
		resp.Body.Close()

		rootTrees = append(rootTrees, ghCommit.Tree.SHA)

		// Queue parents if under depth limit
		if depth == -1 || item.depth < depth {
			for _, p := range ghCommit.Parents {
				queue = append(queue, QueueItem{sha: p.SHA, depth: item.depth + 1})
			}
		}
	}

	fmt.Printf("Found %d tree(s) across %d commits.\n", len(rootTrees), len(visited))

	// 3. Scan trees for blobs
	uniqueBlobs := make(map[string]bool)
	for _, treeSHA := range rootTrees {
		treeURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/trees/%s?recursive=true", owner, repo, treeSHA)
		resp, err := sendGitHubRequest(ctx, httpClient, token, "GET", treeURL, "application/vnd.github.v3+json")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching recursive tree %s: %v\n", treeSHA, err)
			os.Exit(1)
		}

		var ghTree GitHubTree
		if err := json.NewDecoder(resp.Body).Decode(&ghTree); err != nil {
			resp.Body.Close()
			fmt.Fprintf(os.Stderr, "Error decoding tree JSON for %s: %v\n", treeSHA, err)
			os.Exit(1)
		}
		resp.Body.Close()

		for _, entry := range ghTree.Tree {
			if entry.Type == "blob" {
				uniqueBlobs[entry.SHA] = true
			}
		}
	}

	fmt.Printf("Discovered %d unique Git blob SHA1s to index.\n", len(uniqueBlobs))

	// Build list of keys to check in KV
	var checkKeys []string
	keyToSHA1Hex := make(map[string]string)
	for sha1Hex := range uniqueBlobs {
		key := "SHA1:" + sha1Hex
		checkKeys = append(checkKeys, key)
		keyToSHA1Hex[key] = sha1Hex
	}

	fmt.Printf("Checking which of the %d blobs are already indexed in KV...\n", len(checkKeys))

	// Batch get from KV
	existingKeys := make(map[string]bool)
	const batchSize = 200
	for i := 0; i < len(checkKeys); i += batchSize {
		end := i + batchSize
		if end > len(checkKeys) {
			end = len(checkKeys)
		}
		chunk := checkKeys[i:end]

		results, err := kvClient.BatchGet(ctx, nil, chunk)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error batch getting keys from KV service: %v\n", err)
			os.Exit(1)
		}

		for k := range results {
			existingKeys[k] = true
		}
	}

	if concurrency <= 0 {
		concurrency = 20
	}

	toProcess := len(checkKeys) - len(existingKeys)
	fmt.Printf("Found %d blobs already indexed. Processing remaining %d blobs with concurrency=%d...\n", len(existingKeys), toProcess, concurrency)

	// 4. Download blobs, compute SHA256 and store mappings
	type task struct {
		key     string
		sha1Hex string
	}

	type result struct {
		key       string
		sha1Hex   string
		sha256Hex string
		err       error
	}

	taskCh := make(chan task, toProcess)
	resultCh := make(chan result, toProcess)

	var workerWg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		workerWg.Add(1)
		go func() {
			defer workerWg.Done()
			for t := range taskCh {
				sha1Hex := t.key[5:]
				blobURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/blobs/%s", owner, repo, sha1Hex)
				resp, err := sendGitHubRequest(ctx, httpClient, token, "GET", blobURL, "application/vnd.github.v3.raw")
				if err != nil {
					resultCh <- result{err: fmt.Errorf("error downloading blob content %s: %w", sha1Hex, err)}
					continue
				}

				hasher := sha256.New()
				if _, err := io.Copy(hasher, resp.Body); err != nil {
					resp.Body.Close()
					resultCh <- result{err: fmt.Errorf("error hashing blob content %s: %w", sha1Hex, err)}
					continue
				}
				resp.Body.Close()

				sha256Bytes := hasher.Sum(nil)
				sha256Hex := hex.EncodeToString(sha256Bytes)

				resultCh <- result{
					key:       t.key,
					sha1Hex:   sha1Hex,
					sha256Hex: sha256Hex,
				}
			}
		}()
	}

	// Send tasks
	for _, key := range checkKeys {
		if existingKeys[key] {
			continue
		}
		taskCh <- task{key: key, sha1Hex: keyToSHA1Hex[key]}
	}
	close(taskCh)

	// Close resultCh when workers are done
	go func() {
		workerWg.Wait()
		close(resultCh)
	}()

	mappings := make(map[string][]byte)
	count := 0

	for res := range resultCh {
		if res.err != nil {
			fmt.Fprintf(os.Stderr, "Worker error: %v\n", res.err)
			os.Exit(1)
		}

		count++
		fmt.Printf("[%d/%d] Hashed blob %s\n", count, toProcess, res.sha1Hex)

		// Key 1: SHA1:<sha1Hex> -> sha256Hex
		mappings[res.key] = []byte(res.sha256Hex)

		// Key 2: SHA256:<sha256Hex> -> sha1Hex
		key2 := "SHA256:" + res.sha256Hex
		mappings[key2] = []byte(res.sha1Hex)

		// Flush mappings in chunks to avoid large payloads / request timeouts
		if len(mappings) >= 200 {
			if err := uploadMappings(ctx, kvClient, mappings); err != nil {
				fmt.Fprintf(os.Stderr, "Error uploading mappings chunk: %v\n", err)
				os.Exit(1)
			}
			mappings = make(map[string][]byte)
		}
	}

	// Flush any remaining mappings
	if len(mappings) > 0 {
		if err := uploadMappings(ctx, kvClient, mappings); err != nil {
			fmt.Fprintf(os.Stderr, "Error uploading final mappings chunk: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("Successfully scanned repository and indexed mappings in KV service.\n")
}

func sendGitHubRequest(ctx context.Context, httpClient *http.Client, token, method, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	} else {
		req.Header.Set("Accept", "application/vnd.github.v3+json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("GitHub API returned 403 Forbidden (you might have hit rate limits). Body: %s", string(body))
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("GitHub API returned status %d. Body: %s", resp.StatusCode, string(body))
	}
	return resp, nil
}

func uploadMappings(ctx context.Context, kvClient *kv.Client, mappings map[string][]byte) error {
	_, err := kvClient.BatchPut(ctx, nil, mappings)
	return err
}
