package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"

	"invariant/internal/config"
	"invariant/internal/content"
	"invariant/internal/discovery"
	"invariant/internal/files"
	"invariant/internal/finder"
	"invariant/internal/fuse"
	"invariant/internal/slots"
	"invariant/internal/storage"
	"invariant/internal/workspace"
)

func runWorkspace(globalCfg *config.InvariantConfig, args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant workspace <create|mount|pull|unmount|branch|merge|rebase> ...\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  create    Create a new workspace\n")
		fmt.Fprintf(os.Stderr, "  mount     Mount a workspace\n")
		fmt.Fprintf(os.Stderr, "  unmount   Unmount a workspace\n")
		fmt.Fprintf(os.Stderr, "  pull      Pull a workspace\n")
		fmt.Fprintf(os.Stderr, "  branch    Branch a workspace\n")
		fmt.Fprintf(os.Stderr, "  merge     Merge a branch workspace back to parent\n")
		fmt.Fprintf(os.Stderr, "  rebase    Rebase a branch workspace onto parent's current state\n")
		os.Exit(1)
	}

	switch args[0] {
	case "create":
		runWorkspaceCreate(globalCfg, args[1:])
	case "mount":
		runWorkspaceMount(globalCfg, args[1:])
	case "unmount":
		runWorkspaceUnmount(globalCfg, args[1:])
	case "pull":
		runWorkspacePull(globalCfg, args[1:])
	case "branch":
		runWorkspaceBranch(globalCfg, args[1:])
	case "merge":
		runWorkspaceMerge(globalCfg, args[1:])
	case "rebase":
		runWorkspaceRebase(globalCfg, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown workspace command: %s\n", args[0])
		os.Exit(1)
	}
}

func runWorkspaceCreate(globalCfg *config.InvariantConfig, args []string) {
	createFlags := flag.NewFlagSet("workspace create", flag.ExitOnError)
	layersFlag := createFlags.String("layers", "", "Comma-separated list of additional layers")
	createOnly := createFlags.Bool("create-only", false, "Create the workspace but do not mount it")
	protectedFlag := createFlags.Bool("protected", false, "Generate an Ed25519 256-bit elliptic curve key pair for the backing slot")

	createFlags.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: invariant workspace create <directory> <content> [-layers layer1,layer2] [-create-only]\n")
		createFlags.PrintDefaults()
	}
	createFlags.Parse(args)

	if createFlags.NArg() < 2 {
		createFlags.Usage()
		os.Exit(1)
	}

	directory := createFlags.Arg(0)
	contentArg := createFlags.Arg(1)

	var layersList []string
	if *layersFlag != "" {
		layersList = strings.Split(*layersFlag, ",")
	}

	dClient, _, aggClient, slotsClient := initClients(globalCfg)

	// In order to prevent asynchronous Kademlia index syncs from racing our immediate local daemon forks,
	// we force the creation process securely through identical caching layers, bridging isolated memory.
	commonFlags := CommonMountFlags{
		CacheSizeMB:     128,
		DiskCacheSizeMB: 1024,
	}
	cachingStorage, _ := SetupCacheStorage(&commonFlags, aggClient)

	// Resolve the initial content object. It could be an address or a slot string.
	// We handle this directly for now or parse it as slot / tree.
	targetLink := content.ContentLink{}

	// simple heuristic: if it's 64 chars, we assume it's a raw block address.
	var hash string
	if len(contentArg) == 64 {
		hash = contentArg
	} else if len(contentArg) > 0 {
		// might be a namespace name
		resolved, err := discovery.ResolveName(context.Background(), dClient, contentArg)
		if err == nil && len(resolved) > 0 {
			hash = resolved
		} else {
			log.Fatalf("failed to resolve content: %s", contentArg)
		}
	} else {
		log.Fatalf("Invalid content provided: %s", contentArg)
	}

	// Try to GET it to see if it's an existing slot, otherwise we assume it's a block
	// and we MUST create a mutable slot for workspaces to be persistent.
	if _, err := slotsClient.Get(context.Background(), hash); err == nil {
		targetLink = content.ContentLink{Address: hash, Slot: true}
	} else {
		var slotID string
		var policy string

		if *protectedFlag {
			fmt.Println("Generating protected slot using Ed25519 (256-bit elliptic curve)...")
			pub, priv, err := ed25519.GenerateKey(nil)
			if err != nil {
				log.Fatalf("Failed to generate key pair: %v", err)
			}
			slotID = hex.EncodeToString(pub)
			policy = "ecc"

			keysDir, err := config.KeysDir()
			if err != nil {
				log.Fatalf("Fatal error: Failed to locate keys directory: %v", err)
			}

			keyPath := filepath.Join(keysDir, fmt.Sprintf("%s.key", slotID))
			if err := os.WriteFile(keyPath, priv, 0600); err != nil {
				log.Fatalf("Fatal error: Failed to save private key to %s: %v", keyPath, err)
			}
			fmt.Printf("Private key securely saved to: %s\n", keyPath)
		} else {
			// Generate a new standard slot for the static block
			b := make([]byte, 32)
			rand.Read(b)
			slotID = hex.EncodeToString(b)
		}

		if err := slotsClient.Create(context.Background(), slotID, hash, policy); err != nil {
			log.Fatalf("failed to create workspace tracking slot: %v", err)
		}
		log.Printf("Created slot %s to track workspace changes\n", slotID)
		targetLink = content.ContentLink{Address: slotID, Slot: true}
	}

	// Create Workspace directory
	err := os.MkdirAll(directory, 0755)
	if err != nil {
		log.Fatalf("failed to create directory: %v", err)
	}

	wsLink, err := workspace.CreateWorkspace(
		context.Background(),
		cachingStorage,
		slotsClient,
		dClient,
		targetLink,
		layersList,
	)
	if err != nil {
		log.Fatalf("failed to create workspace layers: %v", err)
	}

	// Create .invariant-workspace file inside
	wsPath := filepath.Join(directory, ".invariant-workspace")
	wsInfo := workspace.WorkspaceInfo{
		Content: wsLink,
	}

	wsFile, err := os.Create(wsPath)
	if err != nil {
		log.Fatalf("failed to create .invariant-workspace: %v", err)
	}
	defer wsFile.Close()

	if err := json.NewEncoder(wsFile).Encode(wsInfo); err != nil {
		log.Fatalf("failed to write .invariant-workspace: %v", err)
	}

	if !*createOnly {
		// invoke mount
		runWorkspaceMount(globalCfg, []string{directory})
	} else {
		log.Printf("Workspace created in %s\n", directory)
	}
}

func runWorkspaceMount(globalCfg *config.InvariantConfig, args []string) {
	mountFlags := flag.NewFlagSet("workspace mount", flag.ExitOnError)
	var commonFlags CommonMountFlags
	commonFlags.Register(mountFlags)
	systemd := mountFlags.Bool("systemd", false, "Remount on boot using systemd")
	foreground := mountFlags.Bool("foreground", false, "Mount directly instead of spawning background task")

	mountFlags.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: invariant workspace mount <directory> [options]\n")
		mountFlags.PrintDefaults()
	}
	mountFlags.Parse(args)

	if mountFlags.NArg() < 1 {
		mountFlags.Usage()
		os.Exit(1)
	}

	directory := mountFlags.Arg(0)
	absDir, err := filepath.Abs(directory)
	if err != nil {
		log.Fatalf("invalid directory path: %v", err)
	}

	if *systemd {
		log.Fatalf("-systemd not fully implemented here yet")
		// would create systemctl service unit
	}

	if !*foreground {
		exe, err := os.Executable()
		if err != nil {
			log.Fatalf("Failed to get executable: %v", err)
		}

		var newArgs []string
		newArgs = append(newArgs, "workspace", "mount", "-foreground")
		for _, arg := range args {
			if arg == directory {
				newArgs = append(newArgs, absDir)
			} else {
				newArgs = append(newArgs, arg)
			}
		}

		logPath := "/tmp/invariant-debug.log"
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			log.Fatalf("Failed to open mount log buffer map natively for path %s: %v", logPath, err)
		}
		cmd := exec.Command(exe, newArgs...)
		cmd.Dir = absDir
		cmd.Stdin = nil
		cmd.Stdout = logFile
		cmd.Stderr = logFile

		r, w, err := os.Pipe()
		if err != nil {
			log.Fatalf("Failed to create readiness pipe: %v", err)
		}
		cmd.ExtraFiles = []*os.File{w}
		cmd.Env = append(os.Environ(), "INVARIANT_READY_FD=3")

		if err := cmd.Start(); err != nil {
			log.Fatalf("Failed to start background mount: %v", err)
		}

		w.Close() // Parent no longer needs the write end

		reader := bufio.NewReader(r)
		success := false
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			if line == "INVARIANT_MOUNT_READY\n" {
				success = true
				break
			}
		}

		if success {
			fmt.Printf("Workspace mounted in background (PID: %d)\n", cmd.Process.Pid)
		} else {
			log.Fatalf("Background mount failed or exited unexpectedly (see %s)", logPath)
		}

		r.Close()
		return
	}
	cacheDir, _ := config.CacheDir()
	pidsDir := filepath.Join(cacheDir, "pids")
	os.MkdirAll(pidsDir, 0700)
	pidHash := sha256.Sum256([]byte(absDir))
	pidPath := filepath.Join(pidsDir, fmt.Sprintf("%x.pid", pidHash))

	var readyPipe *os.File
	if fdStr := os.Getenv("INVARIANT_READY_FD"); fdStr != "" {
		if fd, err := strconv.Atoi(fdStr); err == nil {
			readyPipe = os.NewFile(uintptr(fd), "readyPipe")
		}
	}

	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		log.Printf("Warning: failed to write pid file: %v", err)
	}
	defer os.Remove(pidPath)

	// Read .invariant-workspace
	wsPath := filepath.Join(absDir, ".invariant-workspace")
	data, err := os.ReadFile(wsPath)
	if err != nil {
		log.Fatalf("Failed to read %s: %v", wsPath, err)
	}

	var wsInfo workspace.WorkspaceInfo
	if err := json.Unmarshal(data, &wsInfo); err != nil {
		log.Fatalf("Invalid workspace file %s: %v", wsPath, err)
	}

	dClient, _, storageClient, slotsClient := initClients(globalCfg)

	// we have the wsInfo.Content point to .invariant-layer structure
	layers, err := workspace.ResolveLayers(context.Background(), slotsClient, storageClient, wsInfo.Content)
	if err != nil {
		log.Fatalf("Failed to resolve layers: %v", err)
	}

	// Setup our file system with the resolved layers. But wait, filesrv setup via
	// SetupFileSystem ignores layers parameter since SetupFileSystem is hardcoded to not use layers
	// Wait, we need to pass Layers to it!

	layers = append([]files.Layer{{
		RootLink: wsInfo.Content,
		ReadOnly: true,
	}}, layers...)

	wsData := data

	finalStorage, localStore := SetupCacheStorage(&commonFlags, storageClient)

	mountConfig := &files.MountConfig{
		InvariantMount:  true,
		CacheDir:        commonFlags.CacheDir,
		IsWorkspace:     true,
		DiscoveryURL:    commonFlags.DiscoveryURL,
		RootAddr:        wsInfo.Content.Address,
		CacheSizeMB:     commonFlags.CacheSizeMB,
		DiskCacheSizeMB: commonFlags.DiskCacheSizeMB,
		OverflowDir:     commonFlags.OverflowDir,
		Compress:        commonFlags.Compress,
		Encrypt:         commonFlags.Encrypt,
		KeyPolicyStr:    commonFlags.KeyPolicyStr,
		WorkspaceInfo:   wsData,
	}

	// We copy the SetupFileSystem logic but insert layers
	filesOpts := files.Options{
		Storage:          finalStorage,
		LocalStorage:     localStore,
		Slots:            slotsClient,
		Discovery:        dClient,
		RootLink:         wsInfo.Content,
		Layers:           layers,
		AutoSyncTimeout:  time.Minute,
		SlotPollInterval: 5 * time.Minute,
		MountConfig:      mountConfig,
	}

	filesrv, err := files.NewInMemoryFiles(filesOpts)
	if err != nil {
		log.Fatalf("Failed to start file system: %v", err)
	}

	rootNode := fuse.NewNode(filesrv, 1)

	var uid, gid uint32
	if currentUser, err := user.Current(); err == nil {
		if parsedUID, err := strconv.ParseUint(currentUser.Uid, 10, 32); err == nil {
			uid = uint32(parsedUID)
		}
		if parsedGID, err := strconv.ParseUint(currentUser.Gid, 10, 32); err == nil {
			gid = uint32(parsedGID)
		}
	}

	server, err := fs.Mount(absDir, rootNode, &fs.Options{
		UID: uid,
		GID: gid,
	})
	if err != nil {
		log.Fatalf("Mount fail: %v\n", err)
	}

	defer func() {
		log.Println("Syncing workspace before shutdown...")
		if err := filesrv.Sync(context.Background(), 1, true); err != nil {
			log.Printf("Warning: failed to sync workspace cleanly: %v\n", err)
		}
		filesrv.Close()
	}()

	log.Printf("Mounted workspace on %s\n", absDir)
	log.Printf("Unmount by calling 'invariant workspace unmount %s'", absDir)

	if readyPipe != nil {
		readyPipe.Write([]byte("INVARIANT_MOUNT_READY\n"))
		readyPipe.Close()
	}

	server.Wait()
}

func runWorkspaceUnmount(globalCfg *config.InvariantConfig, args []string) {
	unmountFlags := flag.NewFlagSet("workspace unmount", flag.ExitOnError)
	systemd := unmountFlags.Bool("systemd", false, "Remove systemd configuration for the mount")

	unmountFlags.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: invariant workspace unmount <directory> [options]\n")
		unmountFlags.PrintDefaults()
	}
	unmountFlags.Parse(args)

	if unmountFlags.NArg() < 1 {
		unmountFlags.Usage()
		os.Exit(1)
	}

	directory := unmountFlags.Arg(0)

	if *systemd {
		// not implemented here but structure is ready
		log.Printf("systemd configuration removal not implemented.")
	}

	cmd := exec.Command("fusermount", "-u", directory)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("Failed to unmount: %v", err)
	}

	absDir, err := filepath.Abs(directory)
	if err == nil {
		cacheDir, _ := config.CacheDir()
		pidHash := sha256.Sum256([]byte(absDir))
		pidPath := filepath.Join(cacheDir, "pids", fmt.Sprintf("%x.pid", pidHash))
		pidData, pidErr := os.ReadFile(pidPath)
		if pidErr == nil {
			if pid, err := strconv.Atoi(string(pidData)); err == nil && pid > 0 {
				process, findErr := os.FindProcess(pid)
				if findErr == nil {
					log.Printf("Waiting for workspace background tasks to synchronize cleanly (PID: %d)...", pid)
					for {
						if sigErr := process.Signal(syscall.Signal(0)); sigErr != nil {
							break
						}
						time.Sleep(100 * time.Millisecond)
					}
				}
			}
			os.Remove(pidPath)
		}
	}

	log.Printf("Unmounted %s", directory)
}

var (
	sharedDClient       discovery.Discovery
	sharedFinderClient  finder.Finder
	sharedStorageClient storage.Storage
	sharedSlotsClient   slots.Slots
)

func initClients(globalCfg *config.InvariantConfig) (discovery.Discovery, finder.Finder, storage.Storage, slots.Slots) {
	if sharedDClient != nil {
		return sharedDClient, sharedFinderClient, sharedStorageClient, sharedSlotsClient
	}

	discoveryURL := globalCfg.Discovery
	dClient := discovery.NewClient(discoveryURL, nil)

	findService := func(kind string) string {
		id, err := dClient.Find(context.Background(), kind, "", 1)
		if err != nil || len(id) == 0 {
			return ""
		}
		return id[0].Address
	}

	finderAddr := findService("finder-v1")
	var finderClient finder.Finder
	var storageClient storage.Storage
	if finderAddr != "" {
		finderClient = finder.NewClient(finderAddr, nil)
		storageClient = storage.NewAggregateClient(finderClient, dClient, 3, 1000)
	} else {
		sAddr := findService("storage-v1")
		if sAddr == "" {
			sAddr, _ = discovery.ResolveName(context.Background(), dClient, "storage-v1")
		}
		if sAddr == "" {
			log.Fatalf("Could not find storage-v1 service")
		}
		storageClient = storage.NewClient(sAddr, nil)
	}

	slotsAddr := findService("slots-v1")
	if slotsAddr == "" {
		slotsAddr, _ = discovery.ResolveName(context.Background(), dClient, "slots-v1")
	}
	if slotsAddr == "" {
		log.Fatalf("Could not find slots-v1 service")
	}
	slotsClient := slots.NewClient(slotsAddr, nil)

	sharedDClient = dClient
	sharedFinderClient = finderClient
	sharedStorageClient = storageClient
	sharedSlotsClient = slotsClient

	return dClient, finderClient, storageClient, slotsClient
}

func runWorkspaceBranch(globalCfg *config.InvariantConfig, args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant workspace branch [parent-directory] <branch-directory>\n")
		os.Exit(1)
	}

	var parentDir string
	var branchDir string
	if len(args) == 1 {
		parentDir = "."
		branchDir = args[0]
	} else {
		parentDir = args[0]
		branchDir = args[1]
	}

	parentAbsDir, err := filepath.Abs(parentDir)
	if err != nil {
		log.Fatalf("invalid parent directory path: %v", err)
	}

	parentWsPath := filepath.Join(parentAbsDir, ".invariant-workspace")
	parentData, err := os.ReadFile(parentWsPath)
	if err != nil {
		log.Fatalf("parent directory is not a valid workspace: failed to read %s: %v", parentWsPath, err)
	}

	var parentWsInfo workspace.WorkspaceInfo
	if err := json.Unmarshal(parentData, &parentWsInfo); err != nil {
		log.Fatalf("invalid parent workspace file: %v", err)
	}

	_, _, storageClient, slotsClient := initClients(globalCfg)

	parentLayers, err := workspace.ResolveLayers(context.Background(), slotsClient, storageClient, parentWsInfo.Content)
	if err != nil {
		log.Fatalf("failed to resolve parent layers: %v", err)
	}

	parentSlotID, err := workspace.GetWorkspaceSlotID(context.Background(), slotsClient, storageClient, parentWsInfo)
	if err != nil {
		log.Fatalf("failed to get parent workspace slot ID: %v", err)
	}

	parentSnapshotHash, err := slotsClient.Get(context.Background(), parentSlotID)
	if err != nil {
		log.Fatalf("failed to resolve parent slot address: %v", err)
	}

	// Generate a new standard slot for the branch
	b := make([]byte, 32)
	rand.Read(b)
	branchSlotID := hex.EncodeToString(b)

	if err := slotsClient.Create(context.Background(), branchSlotID, parentSnapshotHash, ""); err != nil {
		log.Fatalf("failed to create branch tracking slot: %v", err)
	}

	// Build the branch layers pointing to branchSlotID instead of parentSlotID
	var branchLayers []files.Layer
	for _, layer := range parentLayers {
		if layer.RootLink.Slot && layer.RootLink.Address == parentSlotID {
			layer.RootLink.Address = branchSlotID
		}
		branchLayers = append(branchLayers, layer)
	}

	branchWsLink, err := workspace.CreateWorkspaceFromLayers(context.Background(), storageClient, slotsClient, branchLayers)
	if err != nil {
		log.Fatalf("failed to create branch workspace layers: %v", err)
	}

	err = os.MkdirAll(branchDir, 0755)
	if err != nil {
		log.Fatalf("failed to create branch directory: %v", err)
	}

	branchWsPath := filepath.Join(branchDir, ".invariant-workspace")
	branchWsInfo := workspace.WorkspaceInfo{
		Content:        branchWsLink,
		ParentSnapshot: parentSnapshotHash,
		ParentSlot:     parentSlotID,
	}

	branchWsFile, err := os.Create(branchWsPath)
	if err != nil {
		log.Fatalf("failed to create branch .invariant-workspace: %v", err)
	}
	defer branchWsFile.Close()

	if err := json.NewEncoder(branchWsFile).Encode(branchWsInfo); err != nil {
		log.Fatalf("failed to write branch .invariant-workspace: %v", err)
	}

	log.Printf("Workspace branch created successfully in %s (slot: %s)\n", branchDir, branchSlotID)
}

func runWorkspaceMerge(globalCfg *config.InvariantConfig, args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant workspace merge [parent-directory] <branch-directory>\n")
		os.Exit(1)
	}

	var parentDir string
	var branchDir string
	if len(args) == 1 {
		parentDir = "."
		branchDir = args[0]
	} else {
		parentDir = args[0]
		branchDir = args[1]
	}

	parentAbsDir, err := filepath.Abs(parentDir)
	if err != nil {
		log.Fatalf("invalid parent directory path: %v", err)
	}
	branchAbsDir, err := filepath.Abs(branchDir)
	if err != nil {
		log.Fatalf("invalid branch directory path: %v", err)
	}

	parentWsPath := filepath.Join(parentAbsDir, ".invariant-workspace")
	parentData, err := os.ReadFile(parentWsPath)
	if err != nil {
		log.Fatalf("parent directory is not a valid workspace: failed to read %s: %v", parentWsPath, err)
	}

	var parentWsInfo workspace.WorkspaceInfo
	if err := json.Unmarshal(parentData, &parentWsInfo); err != nil {
		log.Fatalf("invalid parent workspace file: %v", err)
	}

	branchWsPath := filepath.Join(branchAbsDir, ".invariant-workspace")
	branchData, err := os.ReadFile(branchWsPath)
	if err != nil {
		log.Fatalf("branch directory is not a valid workspace: failed to read %s: %v", branchWsPath, err)
	}

	var branchWsInfo workspace.WorkspaceInfo
	if err := json.Unmarshal(branchData, &branchWsInfo); err != nil {
		log.Fatalf("invalid branch workspace file: %v", err)
	}

	if branchWsInfo.ParentSlot == "" || branchWsInfo.ParentSnapshot == "" {
		log.Fatalf("workspace %s is not a branch (missing parent tracking metadata)", branchDir)
	}

	_, _, storageClient, slotsClient := initClients(globalCfg)

	parentSlotID, err := workspace.GetWorkspaceSlotID(context.Background(), slotsClient, storageClient, parentWsInfo)
	if err != nil {
		log.Fatalf("failed to get parent workspace slot ID: %v", err)
	}

	branchSlotID, err := workspace.GetWorkspaceSlotID(context.Background(), slotsClient, storageClient, branchWsInfo)
	if err != nil {
		log.Fatalf("failed to get branch workspace slot ID: %v", err)
	}

	if parentSlotID != branchWsInfo.ParentSlot {
		log.Fatalf("conflict: branch parent slot %s does not match destination parent slot %s", branchWsInfo.ParentSlot, parentSlotID)
	}

	parentCurrentAddress, err := slotsClient.Get(context.Background(), parentSlotID)
	if err != nil {
		log.Fatalf("failed to get parent workspace slot address: %v", err)
	}

	branchCurrentAddress, err := slotsClient.Get(context.Background(), branchSlotID)
	if err != nil {
		log.Fatalf("failed to get branch workspace slot address: %v", err)
	}

	ancestorAddress := branchWsInfo.ParentSnapshot

	log.Printf("Merging branch workspace %s into parent workspace %s...\n", branchDir, parentDir)
	log.Printf("  Parent Slot:       %s (current hash: %s)\n", parentSlotID, parentCurrentAddress)
	log.Printf("  Branch Slot:       %s (current hash: %s)\n", branchSlotID, branchCurrentAddress)
	log.Printf("  Ancestor Snapshot: %s\n", ancestorAddress)

	mergedRootAddress, conflicts, err := workspace.MergeTrees(
		context.Background(),
		ancestorAddress,
		parentCurrentAddress,
		branchCurrentAddress,
		storageClient,
		slotsClient,
	)
	if err != nil {
		log.Fatalf("failed to perform tree merge: %v", err)
	}

	if len(conflicts) > 0 {
		fmt.Fprintf(os.Stderr, "Merge failed: conflicts detected at the following paths:\n")
		for _, conf := range conflicts {
			fmt.Fprintf(os.Stderr, "  - %s\n", conf)
		}
		os.Exit(2)
	}

	err = slotsClient.Update(context.Background(), parentSlotID, mergedRootAddress, parentCurrentAddress, nil)
	if err != nil {
		log.Fatalf("failed to update parent slot: %v", err)
	}

	log.Printf("Successfully merged branch into parent! Parent slot updated to %s\n", mergedRootAddress)
}

func runWorkspaceRebase(globalCfg *config.InvariantConfig, args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant workspace rebase <parent-directory> [branch-directory]\n")
		os.Exit(1)
	}

	parentDir := args[0]
	branchDir := "."
	if len(args) > 1 {
		branchDir = args[1]
	}

	parentAbsDir, err := filepath.Abs(parentDir)
	if err != nil {
		log.Fatalf("invalid parent directory path: %v", err)
	}
	branchAbsDir, err := filepath.Abs(branchDir)
	if err != nil {
		log.Fatalf("invalid branch directory path: %v", err)
	}

	parentWsPath := filepath.Join(parentAbsDir, ".invariant-workspace")
	parentData, err := os.ReadFile(parentWsPath)
	if err != nil {
		log.Fatalf("parent directory is not a valid workspace: failed to read %s: %v", parentWsPath, err)
	}

	var parentWsInfo workspace.WorkspaceInfo
	if err := json.Unmarshal(parentData, &parentWsInfo); err != nil {
		log.Fatalf("invalid parent workspace file: %v", err)
	}

	branchWsPath := filepath.Join(branchAbsDir, ".invariant-workspace")
	branchData, err := os.ReadFile(branchWsPath)
	if err != nil {
		log.Fatalf("branch directory is not a valid workspace: failed to read %s: %v", branchWsPath, err)
	}

	var branchWsInfo workspace.WorkspaceInfo
	if err := json.Unmarshal(branchData, &branchWsInfo); err != nil {
		log.Fatalf("invalid branch workspace file: %v", err)
	}

	if branchWsInfo.ParentSlot == "" || branchWsInfo.ParentSnapshot == "" {
		log.Fatalf("workspace %s is not a branch (missing parent tracking metadata)", branchDir)
	}

	_, _, storageClient, slotsClient := initClients(globalCfg)

	parentSlotID, err := workspace.GetWorkspaceSlotID(context.Background(), slotsClient, storageClient, parentWsInfo)
	if err != nil {
		log.Fatalf("failed to get parent workspace slot ID: %v", err)
	}

	branchSlotID, err := workspace.GetWorkspaceSlotID(context.Background(), slotsClient, storageClient, branchWsInfo)
	if err != nil {
		log.Fatalf("failed to get branch workspace slot ID: %v", err)
	}

	if parentSlotID != branchWsInfo.ParentSlot {
		log.Fatalf("conflict: branch parent slot %s does not match specified parent slot %s", branchWsInfo.ParentSlot, parentSlotID)
	}

	parentCurrentAddress, err := slotsClient.Get(context.Background(), parentSlotID)
	if err != nil {
		log.Fatalf("failed to get parent workspace slot address: %v", err)
	}

	branchCurrentAddress, err := slotsClient.Get(context.Background(), branchSlotID)
	if err != nil {
		log.Fatalf("failed to get branch workspace slot address: %v", err)
	}

	ancestorAddress := branchWsInfo.ParentSnapshot

	log.Printf("Rebasing branch workspace %s onto parent workspace %s...\n", branchDir, parentDir)
	log.Printf("  Parent Slot:       %s (current hash: %s)\n", parentSlotID, parentCurrentAddress)
	log.Printf("  Branch Slot:       %s (current hash: %s)\n", branchSlotID, branchCurrentAddress)
	log.Printf("  Ancestor Snapshot: %s\n", ancestorAddress)

	mergedRootAddress, conflicts, err := workspace.MergeTrees(
		context.Background(),
		ancestorAddress,
		parentCurrentAddress,
		branchCurrentAddress,
		storageClient,
		slotsClient,
	)
	if err != nil {
		log.Fatalf("failed to perform tree merge for rebase: %v", err)
	}

	if len(conflicts) > 0 {
		fmt.Fprintf(os.Stderr, "Rebase failed: conflicts detected at the following paths:\n")
		for _, conf := range conflicts {
			fmt.Fprintf(os.Stderr, "  - %s\n", conf)
		}
		os.Exit(2)
	}

	err = slotsClient.Update(context.Background(), branchSlotID, mergedRootAddress, branchCurrentAddress, nil)
	if err != nil {
		log.Fatalf("failed to update branch slot: %v", err)
	}

	// Update branchWsInfo ParentSnapshot to parentCurrentAddress
	branchWsInfo.ParentSnapshot = parentCurrentAddress

	// Write branchWsInfo back to .invariant-workspace file
	branchWsFile, err := os.Create(branchWsPath)
	if err != nil {
		log.Fatalf("failed to create branch .invariant-workspace: %v", err)
	}
	defer branchWsFile.Close()

	if err := json.NewEncoder(branchWsFile).Encode(branchWsInfo); err != nil {
		log.Fatalf("failed to write branch .invariant-workspace: %v", err)
	}

	log.Printf("Successfully rebased branch! Branch slot updated to %s, parent snapshot advanced to %s\n", mergedRootAddress, parentCurrentAddress)
}
