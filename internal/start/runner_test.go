package start

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrefixWriter(t *testing.T) {
	var buf bytes.Buffer
	w := &prefixWriter{name: "inv-test", out: &buf}

	if _, err := w.Write([]byte("hello ")); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output before newline, got %q", buf.String())
	}

	if _, err := w.Write([]byte("world\nsecond\n")); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	got := buf.String()
	want := "[inv-test:-1] hello world\n[inv-test:-1] second\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRunner_StartAndCancelCleanly(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "dummy-svc")
	script := "#!/bin/sh\nexec sleep 10\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write dummy script: %v", err)
	}

	r := &Runner{
		rc: RunnerConfig{
			MaxBackoffDuration: time.Second,
			RetryInterval:      time.Second,
			Config: &Config{
				Services: []ServiceConfig{
					{
						Command:     "dummy-svc",
						Args:        map[string]string{},
						Environment: map[string]string{"TEST_ENV": "1"},
					},
				},
			},
		},
		baseDir: dir,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		r.Start(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Runner.Start did not exit cleanly after context cancellation")
	}
}

func TestNewRunner(t *testing.T) {
	r, err := NewRunner(RunnerConfig{Config: &Config{}})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	if r == nil || strings.TrimSpace(r.baseDir) == "" {
		t.Fatalf("expected valid runner with non-empty baseDir, got %+v", r)
	}
}
