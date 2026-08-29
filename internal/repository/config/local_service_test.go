package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"invariant/internal/names"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func TestLocalService_RepoConfig(t *testing.T) {
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotsClient := slots.NewMemorySlots("test-slots")
	namesClient := names.NewInMemoryNames()

	svc := NewLocalService(store, slotsClient, namesClient)
	repoName := "myproject"

	// 1. Initial Get should return default branch "main"
	branch, err := svc.GetConfig(ctx, repoName, "default_branch")
	if err != nil || branch != "main" {
		t.Fatalf("GetConfig default_branch: got %s, err %v", branch, err)
	}

	// 2. Set custom setting
	if err := svc.SetConfig(ctx, repoName, "write_tag", "fast-nvme"); err != nil {
		t.Fatalf("SetConfig write_tag failed: %v", err)
	}
	if err := svc.SetConfig(ctx, repoName, "review_required", "true"); err != nil {
		t.Fatalf("SetConfig review_required failed: %v", err)
	}
	if err := svc.SetConfig(ctx, repoName, "ci_runner", "github-actions"); err != nil {
		t.Fatalf("SetConfig ci_runner failed: %v", err)
	}

	// 3. Get updated settings
	tagVal, err := svc.GetConfig(ctx, repoName, "write_tag")
	if err != nil || tagVal != "fast-nvme" {
		t.Errorf("GetConfig write_tag: got %s, err %v", tagVal, err)
	}
	reviewVal, err := svc.GetConfig(ctx, repoName, "review_required")
	if err != nil || reviewVal != "true" {
		t.Errorf("GetConfig review_required: got %s, err %v", reviewVal, err)
	}
	ciVal, err := svc.GetConfig(ctx, repoName, "ci_runner")
	if err != nil || ciVal != "github-actions" {
		t.Errorf("GetConfig ci_runner: got %s, err %v", ciVal, err)
	}

	// 4. List settings
	allSettings, err := svc.ListConfig(ctx, repoName)
	if err != nil {
		t.Fatalf("ListConfig failed: %v", err)
	}
	if allSettings["write_tag"] != "fast-nvme" || allSettings["ci_runner"] != "github-actions" {
		t.Errorf("Unexpected ListConfig result: %+v", allSettings)
	}

	// 5. Unset custom setting
	if err := svc.UnsetConfig(ctx, repoName, "ci_runner"); err != nil {
		t.Fatalf("UnsetConfig failed: %v", err)
	}
	if _, err := svc.GetConfig(ctx, repoName, "ci_runner"); err == nil {
		t.Errorf("Expected ci_runner to be unset")
	}
}

func TestLocalService_GlobalConfig(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	ctx := context.Background()
	svc := NewLocalService(nil, nil, nil)

	// 1. Set global config
	if err := svc.SetConfig(ctx, "", "user.name", "Alice"); err != nil {
		t.Fatalf("SetConfig global user.name failed: %v", err)
	}
	if err := svc.SetConfig(ctx, "", "user.email", "alice@example.com"); err != nil {
		t.Fatalf("SetConfig global user.email failed: %v", err)
	}

	// 2. Get global config
	nameVal, err := svc.GetConfig(ctx, "", "user.name")
	if err != nil || nameVal != "Alice" {
		t.Errorf("GetConfig global user.name: got %s, err %v", nameVal, err)
	}

	// 3. List global config
	list, err := svc.ListConfig(ctx, "")
	if err != nil || list["user.name"] != "Alice" || list["user.email"] != "alice@example.com" {
		t.Errorf("ListConfig global: got %+v, err %v", list, err)
	}

	// 4. Unset global config
	if err := svc.UnsetConfig(ctx, "", "user.email"); err != nil {
		t.Fatalf("UnsetConfig global user.email failed: %v", err)
	}
	if _, err := svc.GetConfig(ctx, "", "user.email"); err == nil {
		t.Errorf("Expected user.email to be unset")
	}

	// Verify file was written
	if _, err := os.Stat(filepath.Join(tempHome, ".invariant", "config.json")); err != nil {
		t.Errorf("Expected ~/.invariant/config.json to exist: %v", err)
	}
}
