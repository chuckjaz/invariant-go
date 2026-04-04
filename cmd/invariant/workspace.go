package main

import (
	"context"
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
		fmt.Fprintf(os.Stderr, "Usage: invariant workspace <create|mount|unmount> ...\n")
		os.Exit(1)
	}

	switch args[0] {
	case "create":
		runWorkspaceCreate(globalCfg, args[1:])
	case "mount":
		runWorkspaceMount(globalCfg, args[1:])
	case "unmount":
		runWorkspaceUnmount(globalCfg, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown workspace command: %s\n", args[0])
		os.Exit(1)
	}
}

func runWorkspaceCreate(globalCfg *config.InvariantConfig, args []string) {
	createFlags := flag.NewFlagSet("workspace create", flag.ExitOnError)
	layersFlag := createFlags.String("layers", "", "Comma-separated list of additional layers")
	createOnly := createFlags.Bool("create-only", false, "Create the workspace but do not mount it")

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

	dClient, _, storageClient, slotsClient := initClients(globalCfg)

	// Resolve the initial content object. It could be an address or a slot string.
	// We handle this directly for now or parse it as slot / tree.
	targetLink := content.ContentLink{}

	// simple heuristic: if it's 64 chars, we assume it's an address, create a slot.
	if len(contentArg) == 64 {
		// Just treat it as address for now, unless we explicitly parse "slot:true" formats.
		// A full implementation would `slotsClient.CreateSlot(contentArg)`. We will just wrap it into a slot manually.
		// Since we don't have enough context on how the CLI normally decides "this is an address, make a slot",
		// We'll just define it as a slot.
		targetLink = content.ContentLink{Address: contentArg, Slot: true}
	} else if len(contentArg) > 0 {
		// might be a namespace name
		resolved, err := discovery.ResolveName(context.Background(), dClient, contentArg)
		if err == nil && len(resolved) > 0 {
			targetLink = content.ContentLink{Address: resolved, Slot: true}
		} else {
			targetLink = content.ContentLink{Address: contentArg}
		}
	} else {
		log.Fatal("Invalid content provided")
	}

	// Create Workspace directory
	err := os.MkdirAll(directory, 0755)
	if err != nil {
		log.Fatalf("failed to create directory: %v", err)
	}

	wsLink, err := workspace.CreateWorkspace(
		context.Background(),
		storageClient,
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
		// A full implementation would fork itself here using os.StartProcess
		// For now we just run in foreground since no daemonizing boilerplate is provided in mount.go
		log.Printf("Note: backgrounding not implemented, running in foreground")
	}

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

	// We copy the SetupFileSystem logic but insert layers
	filesOpts := files.Options{
		Storage:   storageClient,
		Slots:     slotsClient,
		Discovery: dClient,
		Layers:    layers,
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

	log.Printf("Mounted workspace on %s\n", absDir)
	log.Printf("Unmount by calling 'invariant workspace unmount %s'", absDir)
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
		id, err := dClient.Find(context.Background(), kind, 1)
		if err != nil {
			log.Fatalf("Could not find %s service: %v", kind, err)
		}
		if len(id) == 0 {
			log.Fatalf("Could not find %s service", kind)
		}
		return id[0].Address
	}

	finderAddr := findService("finder-v1")
	finderClient := finder.NewClient(finderAddr, nil)
	storageClient := storage.NewAggregateClient(finderClient, dClient, 3, 1000)

	slotsAddr := findService("slots-v1")
	slotsClient := slots.NewClient(slotsAddr, nil)

	sharedDClient = dClient
	sharedFinderClient = finderClient
	sharedStorageClient = storageClient
	sharedSlotsClient = slotsClient

	return dClient, finderClient, storageClient, slotsClient
}
