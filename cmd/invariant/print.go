package main

import (
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
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func runPrint(globalCfg *config.InvariantConfig, args []string) {
	fsFlags := flag.NewFlagSet("print", flag.ExitOnError)
	var discoveryURL string
	fsFlags.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")

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

	findService := func(kind string) string {
		id, err := dClient.Find(kind, 1)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not find %s service: %v\n", kind, err)
			os.Exit(1)
		}
		if len(id) == 0 {
			fmt.Fprintf(os.Stderr, "Could not find %s service\n", kind)
			os.Exit(1)
		}
		return id[0].Address
	}

	finderAddr := findService("finder-v1")
	finderClient := finder.NewClient(finderAddr, nil)
	storageClient := storage.NewAggregateClient(finderClient, dClient, 3, 1000)

	var link content.ContentLink
	if strings.HasPrefix(strings.TrimSpace(targetAddr), "{") {
		err := json.Unmarshal([]byte(targetAddr), &link)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: argument looks like JSON but failed to parse as ContentLink: %v\n", err)
			os.Exit(1)
		}
	} else {
		resolved, err := discovery.ResolveName(dClient, targetAddr)
		if err == nil && resolved != "" {
			targetAddr = resolved
		}
		link.Address = targetAddr
	}

	slotsAddr := findService("slots-v1")
	slotsClient := slots.NewClient(slotsAddr, nil)

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
