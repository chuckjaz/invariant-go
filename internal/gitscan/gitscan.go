package gitscan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// KVScannerClient specifies the interface needed to interact with the KV service during scans.
// BatchGet returns an interface{} containing the map of results to prevent import cycle with the kv package.
type KVScannerClient interface {
	BatchGet(ctx context.Context, txID *uint64, keys []string) (interface{}, error)
	BatchPut(ctx context.Context, txID *uint64, kvs map[string][]byte) (uint64, error)
}

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

func logf(w io.Writer, format string, args ...interface{}) {
	if w != nil {
		fmt.Fprintf(w, format, args...)
	}
}

// extractBatchGetResults extracts the string-to-byte-slice map from the BatchGet results via reflection.
func extractBatchGetResults(results interface{}) map[string][]byte {
	out := make(map[string][]byte)
	v := reflect.ValueOf(results)
	if v.Kind() != reflect.Map {
		return out
	}
	for _, keyVal := range v.MapKeys() {
		keyStr := keyVal.String()
		mapVal := v.MapIndex(keyVal)
		if mapVal.Kind() == reflect.Struct {
			valField := mapVal.FieldByName("Value")
			if valField.IsValid() && valField.Kind() == reflect.Slice {
				out[keyStr] = valField.Bytes()
			}
		}
	}
	return out
}

// ScanLocal scans a local repository starting from commit, indexing Git SHA1 to SHA256 mappings in the KV service.
func ScanLocal(ctx context.Context, kvClient KVScannerClient, localPath, commit string, depth, concurrency int, logWriter io.Writer) error {
	logf(logWriter, "Opening local git repository at %s...\n", localPath)
	r, err := git.PlainOpen(localPath)
	if err != nil {
		return fmt.Errorf("error opening git repository: %w", err)
	}

	hash, err := r.ResolveRevision(plumbing.Revision(commit))
	if err != nil {
		h := plumbing.NewHash(commit)
		hash = &h
	}

	logf(logWriter, "Traversing local commit history starting from commit %s (depth limit: %d)...\n", hash, depth)

	type QueueItem struct {
		hash  plumbing.Hash
		depth int
	}

	queue := []QueueItem{{hash: *hash, depth: 1}}
	visited := make(map[plumbing.Hash]bool)
	var rootTrees []*object.Tree

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		if visited[item.hash] {
			continue
		}
		visited[item.hash] = true

		commitObj, err := r.CommitObject(item.hash)
		if err != nil {
			return fmt.Errorf("error fetching commit %s: %w", item.hash, err)
		}

		tree, err := commitObj.Tree()
		if err != nil {
			return fmt.Errorf("error getting tree for commit %s: %w", item.hash, err)
		}
		rootTrees = append(rootTrees, tree)

		if depth == -1 || item.depth < depth {
			for _, parentHash := range commitObj.ParentHashes {
				queue = append(queue, QueueItem{hash: parentHash, depth: item.depth + 1})
			}
		}
	}

	logf(logWriter, "Found %d tree(s) across %d commits.\n", len(rootTrees), len(visited))

	uniqueBlobs := make(map[string]*object.File)
	traversedTrees := make(map[string]bool)
	scannedTreeCache := make(map[string]bool)
	alreadyScannedTrees := make(map[string]bool)

	var walkTree func(t *object.Tree) error
	walkTree = func(t *object.Tree) error {
		sha1Hex := t.Hash.String()
		scanned, err := isTreeScanned(ctx, kvClient, sha1Hex, scannedTreeCache)
		if err != nil {
			return err
		}
		if scanned {
			alreadyScannedTrees[sha1Hex] = true
			return nil
		}

		traversedTrees[sha1Hex] = true

		for _, entry := range t.Entries {
			if entry.Mode == filemode.Dir {
				subtree, err := r.TreeObject(entry.Hash)
				if err != nil {
					return fmt.Errorf("error getting tree object %s: %w", entry.Hash, err)
				}
				if err := walkTree(subtree); err != nil {
					return err
				}
			} else if entry.Mode.IsFile() {
				fSha1Hex := entry.Hash.String()
				if _, exists := uniqueBlobs[fSha1Hex]; !exists {
					blob, err := r.BlobObject(entry.Hash)
					if err != nil {
						return fmt.Errorf("error getting blob object %s: %w", entry.Hash, err)
					}
					uniqueBlobs[fSha1Hex] = object.NewFile(entry.Name, entry.Mode, blob)
				}
			}
		}
		return nil
	}

	for _, tree := range rootTrees {
		if err := walkTree(tree); err != nil {
			return fmt.Errorf("error traversing tree %s: %w", tree.Hash, err)
		}
	}

	logf(logWriter, "Detected %d tree(s) that were already scanned.\n", len(alreadyScannedTrees))
	logf(logWriter, "Discovered %d unique Git blob SHA1s to index.\n", len(uniqueBlobs))

	var checkKeys []string
	keyToSHA1Hex := make(map[string]string)
	for sha1Hex := range uniqueBlobs {
		key := "SHA1:" + sha1Hex
		checkKeys = append(checkKeys, key)
		keyToSHA1Hex[key] = sha1Hex
	}

	logf(logWriter, "Checking which of the %d blobs are already indexed in KV...\n", len(checkKeys))

	existingKeys := make(map[string]bool)
	const batchSize = 200
	for i := 0; i < len(checkKeys); i += batchSize {
		end := i + batchSize
		if end > len(checkKeys) {
			end = len(checkKeys)
		}
		chunk := checkKeys[i:end]

		resultsInterface, err := kvClient.BatchGet(ctx, nil, chunk)
		if err != nil {
			return fmt.Errorf("error batch getting keys from KV service: %w", err)
		}
		results := extractBatchGetResults(resultsInterface)

		for k := range results {
			existingKeys[k] = true
		}
	}

	if concurrency <= 0 {
		concurrency = 20
	}

	toProcess := len(checkKeys) - len(existingKeys)
	logf(logWriter, "Found %d blobs already indexed. Processing remaining %d blobs with concurrency=%d...\n", len(existingKeys), toProcess, concurrency)

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
				fileObj := uniqueBlobs[sha1Hex]

				reader, err := fileObj.Reader()
				if err != nil {
					resultCh <- result{err: fmt.Errorf("error reading local blob content %s: %w", sha1Hex, err)}
					continue
				}

				hasher := sha256.New()
				if _, err := io.Copy(hasher, reader); err != nil {
					reader.Close()
					resultCh <- result{err: fmt.Errorf("error hashing local blob content %s: %w", sha1Hex, err)}
					continue
				}
				reader.Close()

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

	for _, key := range checkKeys {
		if existingKeys[key] {
			continue
		}
		taskCh <- task{key: key, sha1Hex: keyToSHA1Hex[key]}
	}
	close(taskCh)

	go func() {
		workerWg.Wait()
		close(resultCh)
	}()

	mappings := make(map[string][]byte)
	count := 0

	for res := range resultCh {
		if res.err != nil {
			return res.err
		}

		count++
		logf(logWriter, "[%d/%d] Hashed blob %s\n", count, toProcess, res.sha1Hex)

		mappings[res.key] = []byte(res.sha256Hex)
		key2 := "SHA256:" + res.sha256Hex
		mappings[key2] = []byte(res.sha1Hex)

		if len(mappings) >= 200 {
			if err := uploadMappings(ctx, kvClient, mappings); err != nil {
				return fmt.Errorf("error uploading mappings chunk: %w", err)
			}
			mappings = make(map[string][]byte)
		}
	}

	if len(mappings) > 0 {
		if err := uploadMappings(ctx, kvClient, mappings); err != nil {
			return fmt.Errorf("error uploading final mappings chunk: %w", err)
		}
	}

	treeMappings := make(map[string][]byte)
	for tHash := range traversedTrees {
		treeMappings["tree:sha1:"+tHash] = []byte("scanned")
	}

	if len(treeMappings) > 0 {
		const treeBatchSize = 200
		chunk := make(map[string][]byte)
		for k, v := range treeMappings {
			chunk[k] = v
			if len(chunk) >= treeBatchSize {
				if err := uploadMappings(ctx, kvClient, chunk); err != nil {
					return fmt.Errorf("error uploading tree status mappings: %w", err)
				}
				chunk = make(map[string][]byte)
			}
		}
		if len(chunk) > 0 {
			if err := uploadMappings(ctx, kvClient, chunk); err != nil {
				return fmt.Errorf("error uploading final tree status mappings: %w", err)
			}
		}
	}

	logf(logWriter, "Successfully scanned repository and indexed mappings in KV service.\n")
	return nil
}

// ScanRemote scans a remote GitHub repository starting from commit, indexing Git SHA1 to SHA256 blob mappings.
func ScanRemote(ctx context.Context, kvClient KVScannerClient, owner, repo, token, commit string, depth, concurrency int, logWriter io.Writer) error {
	httpClient := http.DefaultClient

	logf(logWriter, "Traversing commit history starting from commit %s (depth limit: %d)...\n", commit, depth)

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

		commitURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/commits/%s", owner, repo, item.sha)
		resp, err := sendGitHubRequest(ctx, httpClient, token, "GET", commitURL, "application/vnd.github.v3+json")
		if err != nil {
			return fmt.Errorf("error fetching commit %s: %w", item.sha, err)
		}

		var ghCommit GitHubCommit
		if err := json.NewDecoder(resp.Body).Decode(&ghCommit); err != nil {
			resp.Body.Close()
			return fmt.Errorf("error decoding commit JSON for %s: %w", item.sha, err)
		}
		resp.Body.Close()

		rootTrees = append(rootTrees, ghCommit.Tree.SHA)

		if depth == -1 || item.depth < depth {
			for _, p := range ghCommit.Parents {
				queue = append(queue, QueueItem{sha: p.SHA, depth: item.depth + 1})
			}
		}
	}

	logf(logWriter, "Found %d tree(s) across %d commits.\n", len(rootTrees), len(visited))

	uniqueBlobs := make(map[string]bool)
	traversedTrees := make(map[string]bool)
	scannedTreeCache := make(map[string]bool)
	alreadyScannedTrees := make(map[string]bool)

	if len(rootTrees) > 0 {
		var checkKeys []string
		for _, tSHA := range rootTrees {
			checkKeys = append(checkKeys, "tree:sha1:"+tSHA)
		}
		resultsInterface, err := kvClient.BatchGet(ctx, nil, checkKeys)
		if err != nil {
			return fmt.Errorf("error batch getting root trees: %w", err)
		}
		results := extractBatchGetResults(resultsInterface)
		for k, val := range results {
			tSHA := k[10:]
			if string(val) == "scanned" {
				scannedTreeCache[tSHA] = true
			}
		}
	}

	for _, treeSHA := range rootTrees {
		if scannedTreeCache[treeSHA] {
			alreadyScannedTrees[treeSHA] = true
			continue
		}

		treeURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/trees/%s?recursive=true", owner, repo, treeSHA)
		resp, err := sendGitHubRequest(ctx, httpClient, token, "GET", treeURL, "application/vnd.github.v3+json")
		if err != nil {
			return fmt.Errorf("error fetching recursive tree %s: %w", treeSHA, err)
		}

		var ghTree GitHubTree
		if err := json.NewDecoder(resp.Body).Decode(&ghTree); err != nil {
			resp.Body.Close()
			return fmt.Errorf("error decoding tree JSON for %s: %w", treeSHA, err)
		}
		resp.Body.Close()

		var subTreeSHAs []string
		pathToSHA := make(map[string]string)
		pathToSHA[""] = treeSHA

		for _, entry := range ghTree.Tree {
			if entry.Type == "tree" {
				pathToSHA[entry.Path] = entry.SHA
				if !scannedTreeCache[entry.SHA] {
					subTreeSHAs = append(subTreeSHAs, entry.SHA)
				}
			}
		}

		if len(subTreeSHAs) > 0 {
			var checkKeys []string
			for _, sha := range subTreeSHAs {
				checkKeys = append(checkKeys, "tree:sha1:"+sha)
			}
			for i := 0; i < len(checkKeys); i += 200 {
				end := i + 200
				if end > len(checkKeys) {
					end = len(checkKeys)
				}
				chunk := checkKeys[i:end]
				resultsInterface, err := kvClient.BatchGet(ctx, nil, chunk)
				if err != nil {
					return fmt.Errorf("error batch getting subtree statuses: %w", err)
				}
				results := extractBatchGetResults(resultsInterface)
				for k, val := range results {
					sha := k[10:]
					if string(val) == "scanned" {
						scannedTreeCache[sha] = true
					}
				}
			}
		}

		isSkipped := func(path string) bool {
			cur := path
			for {
				if sha, exists := pathToSHA[cur]; exists {
					if scannedTreeCache[sha] {
						return true
					}
				}
				if cur == "" {
					break
				}
				idx := strings.LastIndex(cur, "/")
				if idx == -1 {
					cur = ""
				} else {
					cur = cur[:idx]
				}
			}
			return false
		}

		for _, entry := range ghTree.Tree {
			if entry.Type == "tree" {
				if scannedTreeCache[entry.SHA] {
					parentPath := ""
					idx := strings.LastIndex(entry.Path, "/")
					if idx != -1 {
						parentPath = entry.Path[:idx]
					}
					if !isSkipped(parentPath) {
						alreadyScannedTrees[entry.SHA] = true
					}
				}
			}

			if isSkipped(entry.Path) {
				continue
			}

			if entry.Type == "tree" {
				traversedTrees[entry.SHA] = true
			} else if entry.Type == "blob" {
				uniqueBlobs[entry.SHA] = true
			}
		}

		traversedTrees[treeSHA] = true
	}

	logf(logWriter, "Detected %d tree(s) that were already scanned.\n", len(alreadyScannedTrees))
	logf(logWriter, "Discovered %d unique Git blob SHA1s to index.\n", len(uniqueBlobs))

	var checkKeys []string
	keyToSHA1Hex := make(map[string]string)
	for sha1Hex := range uniqueBlobs {
		key := "SHA1:" + sha1Hex
		checkKeys = append(checkKeys, key)
		keyToSHA1Hex[key] = sha1Hex
	}

	logf(logWriter, "Checking which of the %d blobs are already indexed in KV...\n", len(checkKeys))

	existingKeys := make(map[string]bool)
	const batchSize = 200
	for i := 0; i < len(checkKeys); i += batchSize {
		end := i + batchSize
		if end > len(checkKeys) {
			end = len(checkKeys)
		}
		chunk := checkKeys[i:end]

		resultsInterface, err := kvClient.BatchGet(ctx, nil, chunk)
		if err != nil {
			return fmt.Errorf("error batch getting keys from KV service: %w", err)
		}
		results := extractBatchGetResults(resultsInterface)

		for k := range results {
			existingKeys[k] = true
		}
	}

	if concurrency <= 0 {
		concurrency = 20
	}

	toProcess := len(checkKeys) - len(existingKeys)
	logf(logWriter, "Found %d blobs already indexed. Processing remaining %d blobs with concurrency=%d...\n", len(existingKeys), toProcess, concurrency)

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

	for _, key := range checkKeys {
		if existingKeys[key] {
			continue
		}
		taskCh <- task{key: key, sha1Hex: keyToSHA1Hex[key]}
	}
	close(taskCh)

	go func() {
		workerWg.Wait()
		close(resultCh)
	}()

	mappings := make(map[string][]byte)
	count := 0

	for res := range resultCh {
		if res.err != nil {
			return res.err
		}

		count++
		logf(logWriter, "[%d/%d] Hashed blob %s\n", count, toProcess, res.sha1Hex)

		mappings[res.key] = []byte(res.sha256Hex)
		key2 := "SHA256:" + res.sha256Hex
		mappings[key2] = []byte(res.sha1Hex)

		if len(mappings) >= 200 {
			if err := uploadMappings(ctx, kvClient, mappings); err != nil {
				return fmt.Errorf("error uploading mappings chunk: %w", err)
			}
			mappings = make(map[string][]byte)
		}
	}

	if len(mappings) > 0 {
		if err := uploadMappings(ctx, kvClient, mappings); err != nil {
			return fmt.Errorf("error uploading final mappings chunk: %w", err)
		}
	}

	treeMappings := make(map[string][]byte)
	for tHash := range traversedTrees {
		treeMappings["tree:sha1:"+tHash] = []byte("scanned")
	}

	if len(treeMappings) > 0 {
		const treeBatchSize = 200
		chunk := make(map[string][]byte)
		for k, v := range treeMappings {
			chunk[k] = v
			if len(chunk) >= treeBatchSize {
				if err := uploadMappings(ctx, kvClient, chunk); err != nil {
					return fmt.Errorf("error uploading tree status mappings: %w", err)
				}
				chunk = make(map[string][]byte)
			}
		}
		if len(chunk) > 0 {
			if err := uploadMappings(ctx, kvClient, chunk); err != nil {
				return fmt.Errorf("error uploading final tree status mappings: %w", err)
			}
		}
	}

	logf(logWriter, "Successfully scanned repository and indexed mappings in KV service.\n")
	return nil
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

func uploadMappings(ctx context.Context, kvClient KVScannerClient, mappings map[string][]byte) error {
	_, err := kvClient.BatchPut(ctx, nil, mappings)
	return err
}

func isTreeScanned(ctx context.Context, kvClient KVScannerClient, sha1Hex string, cache map[string]bool) (bool, error) {
	if scanned, ok := cache[sha1Hex]; ok {
		return scanned, nil
	}
	resultsInterface, err := kvClient.BatchGet(ctx, nil, []string{"tree:sha1:" + sha1Hex})
	if err != nil {
		return false, err
	}
	results := extractBatchGetResults(resultsInterface)
	val, ok := results["tree:sha1:"+sha1Hex]
	scanned := ok && string(val) == "scanned"
	cache[sha1Hex] = scanned
	return scanned, nil
}
