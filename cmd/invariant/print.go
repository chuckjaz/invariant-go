package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"invariant/internal/config"
	"invariant/internal/content"
	"invariant/internal/discovery"
	"invariant/internal/finder"
	"invariant/internal/names"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func runPrint(globalCfg *config.InvariantConfig, args []string) {
	fsFlags := flag.NewFlagSet("print", flag.ExitOnError)
	var discoveryURL string
	var cmFlags CommonMountFlags
	fsFlags.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	fsFlags.IntVar(&cmFlags.CacheSizeMB, "cache", 128, "In-memory caching size in MB for storage backend (0 to disable)")
	fsFlags.IntVar(&cmFlags.DiskCacheSizeMB, "disk-cache", 1024, "Disk caching size in MB for storage backend (0 to disable)")
	fsFlags.StringVar(&cmFlags.CacheDir, "cache-dir", "", "Directory to use for the disk cache (default: ~/.cache/invariant)")
	fsFlags.StringVar(&cmFlags.OverflowDir, "overflow-dir", "", "Directory to use for the overflow cache (default: ~/.cache/invariant/overflow)")

	fsFlags.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: invariant print [options] <id-or-name>\n\n")
		fsFlags.PrintDefaults()
	}
	fsFlags.Parse(args)

	if fsFlags.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "Block ID or name is required\n")
		fsFlags.Usage()
		os.Exit(1)
	}
	targetAddr := fsFlags.Arg(0)

	if discoveryURL == "" && globalCfg != nil {
		discoveryURL = globalCfg.Discovery
	}
	if discoveryURL == "" {
		fmt.Fprintf(os.Stderr, "Discovery URL is required\n")
		os.Exit(1)
	}

	var dClient discovery.Discovery
	dClient = discovery.NewClient(discoveryURL, nil)

	descs, err := dClient.Find(context.Background(), "", 1000)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not reach discovery service: %v\n", err)
		os.Exit(1)
	}

	servicesByProtocol := make(map[string]string)
	for _, d := range descs {
		for _, p := range d.Protocols {
			if _, exists := servicesByProtocol[p]; !exists {
				servicesByProtocol[p] = d.Address
			}
		}
	}

	finderAddr := servicesByProtocol["finder-v1"]
	if finderAddr == "" {
		fmt.Fprintf(os.Stderr, "Could not find finder-v1 service\n")
		os.Exit(1)
	}
	finderClient := finder.NewClient(finderAddr, nil)
	baseStorageClient := storage.NewAggregateClient(finderClient, dClient, 3, 1000)
	storageClient, _ := SetupCacheStorage(&cmFlags, baseStorageClient)

	var link content.ContentLink
	if strings.HasPrefix(strings.TrimSpace(targetAddr), "{") {
		err := json.Unmarshal([]byte(targetAddr), &link)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: argument looks like JSON but failed to parse as ContentLink: %v\n", err)
			os.Exit(1)
		}
	} else {
		// Bypass sequential `discovery.ResolveName` by directly leveraging pre-fetched names-v1 endpoints
		namesAddr := servicesByProtocol["names-v1"]
		if namesAddr != "" {
			nameClient := names.NewClient(namesAddr, nil)
			entry, err := nameClient.Get(context.Background(), targetAddr)
			if err == nil && entry.Value != "" {
				targetAddr = entry.Value
			}
		}
		link.Address = targetAddr
	}

	slotsAddr := servicesByProtocol["slots-v1"]
	var slotsClient slots.Slots
	if slotsAddr != "" {
		slotsClient = slots.NewClient(slotsAddr, nil)
	}

	reader, err := content.Read(link, storageClient, slotsClient)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Block not found or failed to read: %v\n", err)
		os.Exit(1)
	}
	defer reader.Close()

	if _, err := io.Copy(os.Stdout, reader); err != nil {
		fmt.Fprintf(os.Stderr, "\nError printing block: %v\n", err)
		os.Exit(1)
	}
}
