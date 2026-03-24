package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"invariant/internal/discovery"
)

func main() {
	var port int
	flag.IntVar(&port, "port", 3003, "Port to listen on")
	var dir string
	flag.StringVar(&dir, "dir", "", "Base directory for file system discovery storage")
	var upstreamURL string
	flag.StringVar(&upstreamURL, "upstream", "", "Upstream discovery service URL to delegate queries to")
	var snapshotInterval time.Duration
	flag.DurationVar(&snapshotInterval, "snapshot-interval", 1*time.Hour, "Interval between snapshots for file system storage")
	flag.Parse()

	var localD discovery.Discovery
	if dir != "" {
		fsd, err := discovery.NewFileSystemDiscovery(dir, snapshotInterval)
		if err != nil {
			log.Fatalf("Failed to initialize file system discovery: %v", err)
		}
		defer fsd.Close()
		localD = fsd
	} else {
		localD = discovery.NewInMemoryDiscovery()
	}

	var d discovery.Discovery
	if upstreamURL != "" {
		parent := discovery.NewClient(upstreamURL, nil)
		d = discovery.NewUpstreamDiscovery(localD, parent)
		log.Printf("Using Upstream discovery delegation pointing to %s", upstreamURL)
	} else {
		d = localD
		if dir != "" {
			log.Printf("Using standalone File System discovery storage at %s", dir)
		} else {
			log.Printf("Using standalone In-Memory discovery storage")
		}
	}

	server := discovery.NewDiscoveryServer(d)

	addr := fmt.Sprintf(":%d", port)
	log.Printf("Discovery service listening on %s...", addr)

	log.Fatal(http.ListenAndServe(addr, server))
}
