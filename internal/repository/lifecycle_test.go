package repository

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"invariant/internal/content"
	"invariant/internal/names"
	"invariant/internal/repository/commit"
	repoconfig "invariant/internal/repository/config"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func setupPhase4TestRepo(t *testing.T) (
	context.Context,
	storage.Storage,
	slots.Slots,
	names.Names,
	commit.Service,
	string,
	string,
) {
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotsClient := slots.NewMemorySlots("test-slots")
	namesClient := names.NewInMemoryNames()

	idProvider := &workflowMockIDProvider{name: "Alice"}
	SetDefaultIdentityProvider(idProvider)
	commitSvc := commit.NewLocalService(store, slotsClient, namesClient, idProvider)

	tempBase := t.TempDir()
	repoName := "lifecyclerepo"
	repoDir := filepath.Join(tempBase, repoName)

	initTree := createTestTree(ctx, store, map[string]string{
		"README.md": "# Lifecycle Repo\n",
	})

	_, _, err := CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      repoName,
		TargetDir: repoDir,
		Content:   initTree,
	})
	if err != nil {
		t.Fatalf("CreateRepository failed: %v", err)
	}

	return ctx, store, slotsClient, namesClient, commitSvc, repoDir, repoName
}

func TestPhase4_BranchManagement(t *testing.T) {
	ctx, store, slotsClient, namesClient, commitSvc, repoDir, repoName := setupPhase4TestRepo(t)
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)

	mainWs := filepath.Join(repoDir, "main")

	// 1. Create a local change branch
	meta1, err := CreateChangeBranch(ctx, store, slotsClient, namesClient, commitSvc, ChangeOptions{
		RepoRoot:   repoDir,
		ChangeName: "feat-a",
		AuthorName: "Alice",
	})
	if err != nil {
		t.Fatalf("CreateChangeBranch feat-a failed: %v", err)
	}

	// 2. Simulate a published peer change branch from Bob in Names service
	bobSlot, _ := AllocateSlot(ctx, slotsClient, meta1.CommitHash, "")
	bobBranchName := ":bob:" + repoName + ":feat-b"
	namesClient.Put(ctx, bobBranchName, bobSlot, nil)

	// Record in repo config so it's discoverable
	cfg, slotID, prevAddr, _ := loadRepoConfigForTag(ctx, store, slotsClient, namesClient, repoName)
	if cfg != nil {
		if cfg.PeerBranches == nil {
			cfg.PeerBranches = make(map[string]string)
		}
		cfg.PeerBranches[bobBranchName] = bobSlot
		_ = saveRepoConfigForTag(ctx, store, slotsClient, namesClient, repoName, cfg, slotID, prevAddr)
	}

	// 3. List branches from feat-a workspace
	branches, err := ListBranches(ctx, store, slotsClient, namesClient, commitSvc, meta1.WorkspaceDir)
	if err != nil {
		t.Fatalf("ListBranches failed: %v", err)
	}

	if len(branches) < 3 {
		t.Fatalf("Expected at least 3 branches (main, feat-a, :bob:...:feat-b), got %d: %+v", len(branches), branches)
	}

	formatted := FormatBranchList(branches)
	if !strings.Contains(formatted, "feat-a") || !strings.Contains(formatted, "bob") || !strings.Contains(formatted, "main") {
		t.Errorf("Unexpected formatted branch list:\n%s", formatted)
	}

	// 4. Delete branch feat-a
	if err := DeleteBranch(ctx, store, slotsClient, namesClient, mainWs, "feat-a"); err != nil {
		t.Fatalf("DeleteBranch failed: %v", err)
	}

	// Verify directory deleted
	if _, err := os.Stat(filepath.Join(repoDir, "feat-a")); !os.IsNotExist(err) {
		t.Errorf("Expected feat-a workspace directory to be deleted")
	}
}

func TestPhase4_Checkout(t *testing.T) {
	ctx, store, slotsClient, namesClient, commitSvc, repoDir, repoName := setupPhase4TestRepo(t)
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)

	mainWs := filepath.Join(repoDir, "main")

	// 1. Create a change branch feat-x and add a file
	metaX, err := CreateChangeBranch(ctx, store, slotsClient, namesClient, commitSvc, ChangeOptions{
		RepoRoot:   repoDir,
		ChangeName: "feat-x",
		AuthorName: "Alice",
	})
	if err != nil {
		t.Fatalf("CreateChangeBranch failed: %v", err)
	}

	os.WriteFile(filepath.Join(metaX.WorkspaceDir, "alice.txt"), []byte("Alice's work\n"), 0644)
	ExecuteCommit(ctx, store, slotsClient, commitSvc, CommitOptions{
		WorkspaceDir: metaX.WorkspaceDir,
		Message:      "Alice commit on feat-x",
		AuthorName:   "Alice",
	})

	// 2. Checkout back to main
	metaMain, err := CheckoutBranch(ctx, store, slotsClient, namesClient, commitSvc, CheckoutOptions{
		WorkspaceDir: metaX.WorkspaceDir,
		BranchName:   "main",
	})
	if err != nil {
		t.Fatalf("CheckoutBranch main failed: %v", err)
	}
	if metaMain.BranchName != "main" {
		t.Errorf("Expected branch main, got %s", metaMain.BranchName)
	}
	if curWd, _ := os.Getwd(); curWd != mainWs {
		t.Errorf("Expected working directory to be main workspace (%s), got %s", mainWs, curWd)
	}

	// 3. Checkout peer branch published by Bob
	bobTree := createTestTree(ctx, store, map[string]string{
		"bob.txt": "Bob's peer contribution\n",
	})
	_, bobCommitHash, _ := commitSvc.CreateCommit(ctx, commit.CreateRequest{
		RepoName:   repoName,
		BranchName: "feat-peer",
		TreeLink:   content.ContentLink{Address: bobTree},
		Message:    "Bob's peer feature",
		Author:     Identity{Name: "Bob"},
	})
	bobSlot, _ := AllocateSlot(ctx, slotsClient, bobCommitHash, "")
	bobBranchName := ":bob:" + repoName + ":feat-peer"
	namesClient.Put(ctx, bobBranchName, bobSlot, nil)

	metaPeer, err := CheckoutBranch(ctx, store, slotsClient, namesClient, commitSvc, CheckoutOptions{
		WorkspaceDir: mainWs,
		BranchName:   bobBranchName,
	})
	if err != nil {
		t.Fatalf("CheckoutBranch peer branch failed: %v", err)
	}

	if metaPeer.BranchName != bobBranchName {
		t.Errorf("Expected branch %s, got %s", bobBranchName, metaPeer.BranchName)
	}
	bobFile := filepath.Join(metaPeer.WorkspaceDir, "bob.txt")
	if data, err := os.ReadFile(bobFile); err != nil || string(data) != "Bob's peer contribution\n" {
		t.Errorf("Peer file not materialized properly: %v, content=%s", err, string(data))
	}
}

func TestPhase4_Tags(t *testing.T) {
	ctx, store, slotsClient, namesClient, commitSvc, repoDir, repoName := setupPhase4TestRepo(t)
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)

	mainWs := filepath.Join(repoDir, "main")

	// 1. Create tag v1.0.0
	tag1, err := CreateTag(ctx, store, slotsClient, namesClient, commitSvc, mainWs, "v1.0.0", "")
	if err != nil {
		t.Fatalf("CreateTag failed: %v", err)
	}
	if tag1.Name != "v1.0.0" || tag1.FullName != repoName+":tags:v1.0.0" {
		t.Errorf("Unexpected tag info: %+v", tag1)
	}

	// 2. Duplicate tag should fail
	if _, err := CreateTag(ctx, store, slotsClient, namesClient, commitSvc, mainWs, "v1.0.0", ""); err == nil {
		t.Errorf("Expected duplicate tag creation to fail, but succeeded")
	}

	// 3. Create second tag v1.1.0
	_, err = CreateTag(ctx, store, slotsClient, namesClient, commitSvc, mainWs, "v1.1.0", "")
	if err != nil {
		t.Fatalf("CreateTag v1.1.0 failed: %v", err)
	}

	// 4. List tags
	tags, err := ListTags(ctx, store, slotsClient, namesClient, commitSvc, mainWs)
	if err != nil {
		t.Fatalf("ListTags failed: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("Expected 2 tags, got %d", len(tags))
	}
	formatted := FormatTagList(tags)
	if !strings.Contains(formatted, "v1.0.0") || !strings.Contains(formatted, "v1.1.0") {
		t.Errorf("Unexpected tag list format:\n%s", formatted)
	}

	// 5. Delete tag
	if err := DeleteTag(ctx, store, slotsClient, namesClient, mainWs, "v1.0.0"); err != nil {
		t.Fatalf("DeleteTag failed: %v", err)
	}
	remainingTags, _ := ListTags(ctx, store, slotsClient, namesClient, commitSvc, mainWs)
	if len(remainingTags) != 1 || remainingTags[0].Name != "v1.1.0" {
		t.Errorf("Unexpected remaining tags: %+v", remainingTags)
	}
}

func TestPhase4_Config(t *testing.T) {
	ctx, store, slotsClient, namesClient, _, repoDir, _ := setupPhase4TestRepo(t)
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)

	mainWs := filepath.Join(repoDir, "main")
	configSvc := repoconfig.NewLocalService(store, slotsClient, namesClient)

	// 1. Set repo config
	if err := SetConfigSetting(ctx, configSvc, mainWs, false, "review_required", "true"); err != nil {
		t.Fatalf("SetConfigSetting failed: %v", err)
	}
	if err := SetConfigSetting(ctx, configSvc, mainWs, false, "custom_editor", "nvim"); err != nil {
		t.Fatalf("SetConfigSetting custom_editor failed: %v", err)
	}

	// 2. Get repo config
	val, err := GetConfigSetting(ctx, configSvc, mainWs, false, "review_required")
	if err != nil || val != "true" {
		t.Errorf("GetConfigSetting review_required: got %s, err %v", val, err)
	}
	editorVal, err := GetConfigSetting(ctx, configSvc, mainWs, false, "custom_editor")
	if err != nil || editorVal != "nvim" {
		t.Errorf("GetConfigSetting custom_editor: got %s, err %v", editorVal, err)
	}

	// 3. List repo config
	settings, err := ListConfigSettings(ctx, configSvc, mainWs, false)
	if err != nil {
		t.Fatalf("ListConfigSettings failed: %v", err)
	}
	if settings["review_required"] != "true" || settings["custom_editor"] != "nvim" {
		t.Errorf("Unexpected settings map: %+v", settings)
	}

	// 4. Unset config
	if err := UnsetConfigSetting(ctx, configSvc, mainWs, false, "custom_editor"); err != nil {
		t.Fatalf("UnsetConfigSetting failed: %v", err)
	}
	if _, err := GetConfigSetting(ctx, configSvc, mainWs, false, "custom_editor"); err == nil {
		t.Errorf("Expected custom_editor to be unset")
	}
}

func TestPhase4_Layers(t *testing.T) {
	ctx, store, slotsClient, namesClient, commitSvc, repoDir, _ := setupPhase4TestRepo(t)
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)

	mainWs := filepath.Join(repoDir, "main")

	// 1. Create a dependency library repository
	libRepoName := "libcommon"
	libDir := filepath.Join(t.TempDir(), libRepoName)
	libTree := createTestTree(ctx, store, map[string]string{
		"common.go": "package common\n\nconst Version = \"1.0.0\"\n",
	})
	CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      libRepoName,
		TargetDir: libDir,
		Content:   libTree,
	})

	// 2. Add layer to mainWs
	layer, err := AddLayer(ctx, store, slotsClient, namesClient, commitSvc, mainWs, libRepoName, "deps/common", "")
	if err != nil {
		t.Fatalf("AddLayer failed: %v", err)
	}
	if layer.Repository != libRepoName || layer.MountPath != "deps/common" {
		t.Errorf("Unexpected layer result: %+v", layer)
	}

	// Verify file was materialized
	commonFile := filepath.Join(mainWs, "deps", "common", "common.go")
	if data, err := os.ReadFile(commonFile); err != nil || !strings.Contains(string(data), "Version = \"1.0.0\"") {
		t.Errorf("Layer file not found or corrupted: %v, content=%s", err, string(data))
	}

	// 3. List layers
	layers, err := ListLayers(mainWs)
	if err != nil {
		t.Fatalf("ListLayers failed: %v", err)
	}
	if len(layers) != 1 || layers[0].MountPath != "deps/common" {
		t.Errorf("Unexpected layer list: %+v", layers)
	}
	formatted := FormatLayerList(layers)
	if !strings.Contains(formatted, "deps/common") || !strings.Contains(formatted, "libcommon") {
		t.Errorf("Unexpected layer list formatting:\n%s", formatted)
	}

	// 4. Remove layer
	if err := RemoveLayer(mainWs, "deps/common"); err != nil {
		t.Fatalf("RemoveLayer failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mainWs, "deps", "common")); !os.IsNotExist(err) {
		t.Errorf("Expected layer directory to be removed")
	}
	remaining, _ := ListLayers(mainWs)
	if len(remaining) != 0 {
		t.Errorf("Expected 0 layers remaining, got %d", len(remaining))
	}
}
