package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
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
	var storagesArg string
	flag.StringVar(&storagesArg, "storage", "", "Comma-separated IDs or Names of storage services")
	var slotsArg string
	flag.StringVar(&slotsArg, "slots", "", "ID or Name of the slots service")
	var rootAddr string
	flag.StringVar(&rootAddr, "root", "", "Root block or slot address")
	var rootIsSlot bool
	flag.BoolVar(&rootIsSlot, "root-is-slot", false, "Whether the root address refers to a slot")
	var port int
	flag.IntVar(&port, "port", 0, "Port to listen on (0 for random available port)")
	flag.Parse()

	var dClient discovery.Discovery
	if discoveryURL != "" {
		dClient = discovery.NewClient(discoveryURL, nil)
	}

	resolveService := func(name string, kind string) string {
		if dClient != nil {
			id, err := discovery.ResolveName(dClient, name)
			if err != nil {
				log.Printf("Warning: Could not resolve %s name %q via names service: %v", kind, name, err)
				id = name // fallback
			}
			if desc, ok := dClient.Get(id); ok {
				return desc.Address
			}
		}
		return name // fallback to treating it as just an address or URL directly if not found
	}

	storages := []string{}
	if storagesArg != "" {
		for _, s := range strings.Split(storagesArg, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				storages = append(storages, s)
			}
		}
	}

	var finderClient finder.Finder

	if dClient == nil {
		// Create a local memory finder with a dummy ID (e.g. all zeros) if none provided
		mf, err := finder.NewMemoryFinder("0000000000000000000000000000000000000000000000000000000000000000")
		if err != nil {
			log.Fatalf("Failed to create memory finder: %v", err)
		}
		finderClient = mf
	}

	var storageClient storage.Storage

	if dClient == nil {
		dClient = discovery.NewInMemoryDiscovery()
	}

	if len(storages) > 0 {
		for i, s := range storages {
			addr := resolveService(s, "storage")
			if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
				addr = "http://" + addr
			}
			id := fmt.Sprintf("static-storage-%d", i)
			dClient.Register(discovery.ServiceRegistration{
				ID:        id,
				Address:   addr,
				Protocols: []string{"storage-v1"},
			})
		}
		storageClient = storage.NewAggregateClient(finderClient, dClient, len(storages), 1000)
	} else {
		// Discover up to 3
		storageClient = storage.NewAggregateClient(finderClient, dClient, 3, 1000)
	}

	var slotsClient slots.Slots
	if slotsArg != "" {
		addr := resolveService(slotsArg, "slots")
		if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
			addr = "http://" + addr
		}
		slotsClient = slots.NewClient(addr, nil)
	} else if discoveryURL != "" {
		// Look up a slot service from discovery if not provided
		services, err := dClient.Find("slots-v1", 1)
		if err == nil && len(services) > 0 {
			slotsClient = slots.NewClient(services[0].Address, nil)
		}
	}

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
