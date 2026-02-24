package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"invariant/internal/discovery"
	"invariant/internal/identity"
	"invariant/internal/names"
)

func main() {
	var dir string
	flag.StringVar(&dir, "dir", "", "Base directory for file system names storage")
	var discoveryURL string
	flag.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	var advertiseAddr string
	flag.StringVar(&advertiseAddr, "advertise", "", "Address to advertise to the discovery service")
	var port int
	flag.IntVar(&port, "port", 0, "Port to listen on (0 for random available port)")
	var snapshotInterval time.Duration
	flag.DurationVar(&snapshotInterval, "snapshot-interval", 1*time.Hour, "Interval between snapshots for file system storage")
	flag.Parse()

	var n names.Names
	if dir != "" {
		fsnd, err := names.NewFileSystemNames(dir, snapshotInterval)
		if err != nil {
			log.Fatalf("Failed to initialize file system names: %v", err)
		}
		defer fsnd.Close()
		n = fsnd
	} else {
		n = names.NewInMemoryNames()
	}

	server := names.NewNamesServer(n)

	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", addr, err)
	}

	actualPort := listener.Addr().(*net.TCPAddr).Port

	if discoveryURL != "" {
		if advertiseAddr == "" {
			advertiseAddr = fmt.Sprintf("http://localhost:%d", actualPort)
		}

		id := n.(identity.Provider).ID()
		client := discovery.NewClient(discoveryURL, nil)

		err := client.Register(discovery.ServiceRegistration{
			ID:        id,
			Address:   advertiseAddr,
			Protocols: []string{"names-v1"},
		})
		if err != nil {
			log.Fatalf("Failed to register with discovery service: %v", err)
		}
		log.Printf("Registered with discovery service %s as %s", discoveryURL, id)
	}

	log.Printf("Listening on :%d...", actualPort)
	if dir != "" {
		log.Printf("Using File System Names storage at %s", dir)
	} else {
		log.Printf("Using In-Memory Names storage")
	}
	log.Fatal(http.Serve(listener, server))
}
