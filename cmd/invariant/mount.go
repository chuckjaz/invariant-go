package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/user"
	"strconv"
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
)

func runMount(globalCfg *config.InvariantConfig, args []string) {
	fsFlags := flag.NewFlagSet("mount", flag.ExitOnError)
	var mountpoint string
	fsFlags.StringVar(&mountpoint, "mount", "", "Directory to mount the FUSE file system")
	var discoveryURL string
	fsFlags.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	var rootAddr string
	fsFlags.StringVar(&rootAddr, "root", "", "Root block or slot address")
	var slot string
	fsFlags.StringVar(&slot, "slot", "", "Whether the root address refers to a slot")

	fsFlags.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: invariant mount [options]\n\n")
		fsFlags.PrintDefaults()
	}
	fsFlags.Parse(args)

	if mountpoint == "" {
		log.Fatalf("Mountpoint is required (--mount)")
	}

	if discoveryURL == "" && globalCfg != nil {
		discoveryURL = globalCfg.Discovery
	}
	if discoveryURL == "" {
		log.Fatalf("Discovery URL is required")
	}

	var dClient discovery.Discovery
	dClient = discovery.NewClient(discoveryURL, nil)

	rootIsSlot := false
	if slot != "" {
		rootAddr = slot
		rootIsSlot = true
	}

	findService := func(kind string) string {
		id, err := dClient.Find(kind, 1)
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

	opts := files.Options{
		Storage: storageClient,
		Slots:   slotsClient,
		RootLink: content.ContentLink{
			Address: rootAddr,
			Slot:    rootIsSlot,
		},
		AutoSyncTimeout:  time.Minute,
		SlotPollInterval: 5 * time.Minute,
	}

	filesrv, err := files.NewInMemoryFiles(opts)
	if err != nil {
		log.Fatalf("Failed to initialize files service: %v", err)
	}
	defer filesrv.Close()

	rootNode := fuse.NewNode(filesrv, 1) // 1 is the root node ID in InMemoryFiles

	var uid, gid uint32
	if currentUser, err := user.Current(); err == nil {
		if parsedUID, err := strconv.ParseUint(currentUser.Uid, 10, 32); err == nil {
			uid = uint32(parsedUID)
		}
		if parsedGID, err := strconv.ParseUint(currentUser.Gid, 10, 32); err == nil {
			gid = uint32(parsedGID)
		}
	}

	server, err := fs.Mount(mountpoint, rootNode, &fs.Options{
		UID: uid,
		GID: gid,
	})
	if err != nil {
		log.Fatalf("Mount fail: %v\n", err)
	}

	log.Printf("Mounted on %s\n", mountpoint)
	log.Printf("Unmount by calling 'fusermount -u %s'", mountpoint)
	server.Wait()
}
