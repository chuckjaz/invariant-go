package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"invariant/internal/content"
	"invariant/internal/discovery"
	"invariant/internal/files"
	"invariant/internal/finder"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func main() {
	var discoveryURL string
	flag.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	var rootAddr string
	flag.StringVar(&rootAddr, "root", "", "Root block or slot address")
	var slot string
	flag.StringVar(&slot, "slot", "", "Whether the root address refers to a slot")
	var port int
	flag.IntVar(&port, "port", 0, "Port to listen on (0 for random available port)")
	flag.Parse()

	var dClient discovery.Discovery
	if discoveryURL != "" {
		dClient = discovery.NewClient(discoveryURL, nil)
	} else {
		log.Fatalf("Discovery URL is required")
	}

	rootIsSlot := false
	if slot != "" {
		rootAddr = slot
		rootIsSlot = true
	}

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

	f, err := files.NewInMemoryFiles(opts)
	if err != nil {
		log.Fatalf("Failed to initialize files service: %v", err)
	}
	defer f.Close()

	server := files.NewServer(f)

	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", addr, err)
	}

	actualPort := listener.Addr().(*net.TCPAddr).Port
	log.Printf("Listening on :%d...", actualPort)
	log.Fatal(http.Serve(listener, server.Handler()))
}
