package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"invariant/internal/content"
	"invariant/internal/filetree"
	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
	"invariant/internal/trace"
)

func runGitCommand(dir string, args ...string) (time.Duration, error) {
	start := time.Now()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=BenchmarkUser",
		"GIT_AUTHOR_EMAIL=benchmark@example.com",
		"GIT_COMMITTER_NAME=BenchmarkUser",
		"GIT_COMMITTER_EMAIL=benchmark@example.com",
	)
	err := cmd.Run()
	return time.Since(start), err
}

func generateSyntheticFiles(dir string, count int, bytesPerFile int) error {
	payload := bytes.Repeat([]byte("a"), bytesPerFile)
	for i := range count {
		filePath := filepath.Join(dir, fmt.Sprintf("sub_%02d", i%10), fmt.Sprintf("file_%04d.txt", i))
		if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(filePath, payload, 0644); err != nil {
			return err
		}
	}
	return nil
}

func TestBenchmark_IRVsGit_Comparison(t *testing.T) {
	ctx := context.Background()
	tracer := trace.NewTracer(1000)

	tempDir := t.TempDir()
	gitDir := filepath.Join(tempDir, "git-bench")
	irDir := filepath.Join(tempDir, "ir-bench")
	_ = os.MkdirAll(gitDir, 0755)
	_ = os.MkdirAll(irDir, 0755)

	store := storage.NewInMemoryStorage()
	slotsClient := slots.NewMemorySlots("bench-slots")
	namesClient := names.NewInMemoryNames()
	idProvider := &workflowMockIDProvider{name: "Benchmarker"}
	SetDefaultIdentityProvider(idProvider)
	commitSvc := commit.NewLocalService(store, slotsClient, namesClient, idProvider)

	// 1. Repo Creation Benchmark
	t.Log("=== Benchmark 1: Repository Creation ===")
	gitInitDur, err := runGitCommand(gitDir, "init", "-b", "main")
	if err != nil {
		t.Fatalf("git init failed: %v", err)
	}

	startIR := time.Now()
	_, doneSpan := StartTraceSpan(ctx, tracer, "ir_create")
	cfg, rootCommitHash, err := CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      "ir-bench",
		TargetDir: irDir,
	})
	doneSpan(err, "repo", "ir-bench")
	irCreateDur := time.Since(startIR)

	if err != nil || rootCommitHash == "" || cfg == nil {
		t.Fatalf("ir create failed: %v", err)
	}
	t.Logf("  git init:  %v", gitInitDur)
	t.Logf("  ir create: %v", irCreateDur)

	// 2. Commit Latency Benchmark (100 files, Small Repo)
	t.Log("=== Benchmark 2: Commit Latency (100 files) ===")
	fileCount := 100
	fileSize := 1024

	// Populate Git repo
	_ = generateSyntheticFiles(gitDir, fileCount, fileSize)
	_, _ = runGitCommand(gitDir, "add", ".")
	gitCommitDur, err := runGitCommand(gitDir, "commit", "-m", "Initial 100 files commit")
	if err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	// Populate IR repo
	var dirEntries filetree.Directory
	for i := range fileCount {
		payload := bytes.Repeat([]byte("a"), fileSize)
		cLink, _ := content.Write(bytes.NewReader(payload), store, content.WriterOptions{})
		dirEntries = append(dirEntries, &filetree.FileEntry{
			BaseEntry: filetree.BaseEntry{
				Name: fmt.Sprintf("file_%04d.txt", i),
				Kind: filetree.FileKind,
			},
			Content: cLink,
			Size:    uint64(len(payload)),
		})
	}
	dirJSON, _ := json.Marshal(dirEntries)
	treeLink, _ := content.Write(bytes.NewReader(dirJSON), store, content.WriterOptions{})

	startIRCommit := time.Now()
	_, doneCommitSpan := StartTraceSpan(ctx, tracer, "ir_commit")
	c1, h1, err := commitSvc.CreateCommit(ctx, commit.CreateRequest{
		RepoName:   "ir-bench",
		BranchName: "main",
		TreeLink:   treeLink,
		Parents:    []string{rootCommitHash},
		Message:    "Initial 100 files commit",
		Author:     Identity{Name: "Benchmarker"},
	})
	doneCommitSpan(err, "files", fmt.Sprintf("%d", fileCount))
	irCommitDur := time.Since(startIRCommit)

	if err != nil || h1 == "" || c1 == nil {
		t.Fatalf("ir commit failed: %v", err)
	}
	t.Logf("  git commit (100 files): %v", gitCommitDur)
	t.Logf("  ir commit  (100 files): %v", irCommitDur)

	// 3. History Traversal Benchmark
	t.Log("=== Benchmark 3: History Traversal ===")
	// Add 50 commits to git and ir
	parentGit := "HEAD"
	parentIR := h1
	for k := range 50 {
		msg := fmt.Sprintf("Commit %d", k)
		// Git commit
		testFile := filepath.Join(gitDir, "history.txt")
		_ = os.WriteFile(testFile, []byte(msg), 0644)
		_, _ = runGitCommand(gitDir, "add", "history.txt")
		_, _ = runGitCommand(gitDir, "commit", "-m", msg)
		_ = parentGit

		// IR commit
		cLink, _ := content.Write(bytes.NewReader([]byte(msg)), store, content.WriterOptions{})
		var subDir filetree.Directory
		subDir = append(subDir, &filetree.FileEntry{
			BaseEntry: filetree.BaseEntry{Name: "history.txt", Kind: filetree.FileKind},
			Content:   cLink,
			Size:      uint64(len(msg)),
		})
		subJSON, _ := json.Marshal(subDir)
		tLink, _ := content.Write(bytes.NewReader(subJSON), store, content.WriterOptions{})
		_, h, _ := commitSvc.CreateCommit(ctx, commit.CreateRequest{
			RepoName:   "ir-bench",
			BranchName: "main",
			TreeLink:   tLink,
			Parents:    []string{parentIR},
			Message:    msg,
			Author:     Identity{Name: "Benchmarker"},
		})
		parentIR = h
	}

	gitLogDur, _ := runGitCommand(gitDir, "log", "-n", "50", "--oneline")
	startIRLog := time.Now()
	_, doneLogSpan := StartTraceSpan(ctx, tracer, "ir_log")
	commits, hashes, err := commitSvc.GetHistory(ctx, parentIR, true, "")
	doneLogSpan(err, "count", fmt.Sprintf("%d", len(commits)))
	irLogDur := time.Since(startIRLog)

	if err != nil || len(hashes) < 50 {
		t.Fatalf("ir log failed: %v, got %d", err, len(hashes))
	}
	t.Logf("  git log (50 commits): %v", gitLogDur)
	t.Logf("  ir log  (50 commits): %v", irLogDur)

	// 4. Bisect Benchmark
	t.Log("=== Benchmark 4: History Bisect ===")
	startIRBisect := time.Now()
	_, doneBisectSpan := StartTraceSpan(ctx, tracer, "ir_bisect")
	midpoint, rem, err := commitSvc.Bisect(ctx, []string{h1}, []string{parentIR})
	doneBisectSpan(err, "midpoint", midpoint)
	irBisectDur := time.Since(startIRBisect)

	if err != nil || midpoint == "" {
		t.Fatalf("ir bisect failed: %v", err)
	}
	t.Logf("  ir bisect (50 commit DAG): %v (midpoint: %s, remaining: %d)", irBisectDur, midpoint[:8], rem)

	// 5. Verify Tracing Spans & Metrics Summary
	summary := tracer.Summary("repository")
	if summary.TotalSpans == 0 {
		t.Errorf("Expected recorded distributed tracing spans, got 0")
	}
	t.Logf("=== Distributed Tracing Summary (Total Spans: %d) ===", summary.TotalSpans)
	for endpoint, stat := range summary.Endpoints {
		t.Logf("  Endpoint [%s]: Count=%d, Min=%.3fms, Mean=%.3fms, P50=%.3fms, P95=%.3fms, Max=%.3fms",
			endpoint, stat.Count, stat.MinMs, stat.MeanMs, stat.P50Ms, stat.P95Ms, stat.MaxMs)
	}
}

func TestTreeManifestCaching(t *testing.T) {
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotsClient := slots.NewMemorySlots("slots-id")

	// Create test tree with 1,000 files
	var dir filetree.Directory
	for i := range 1000 {
		data := fmt.Appendf(nil, "file content %d", i)
		cLink, _ := content.Write(bytes.NewReader(data), store, content.WriterOptions{})
		dir = append(dir, &filetree.FileEntry{
			BaseEntry: filetree.BaseEntry{Name: fmt.Sprintf("file_%04d.txt", i), Kind: filetree.FileKind},
			Content:   cLink,
			Size:      uint64(len(data)),
		})
	}
	dirJSON, _ := json.Marshal(dir)
	treeLink, _ := content.Write(bytes.NewReader(dirJSON), store, content.WriterOptions{})

	// Flatten tree twice to test flattening & parsing throughput
	start1 := time.Now()
	entries1, err := commit.FlattenFileTree(ctx, treeLink.Address, store, slotsClient)
	dur1 := time.Since(start1)
	if err != nil || len(entries1) != 1000 {
		t.Fatalf("FlattenFileTree 1 failed: %v", err)
	}

	start2 := time.Now()
	entries2, err := commit.FlattenFileTree(ctx, treeLink.Address, store, slotsClient)
	dur2 := time.Since(start2)
	if err != nil || len(entries2) != 1000 {
		t.Fatalf("FlattenFileTree 2 failed: %v", err)
	}

	t.Logf("Tree Flattening 1,000 files: pass1=%v, pass2=%v", dur1, dur2)
}
