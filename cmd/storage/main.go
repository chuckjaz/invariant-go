package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"invariant/internal/discovery"
	"invariant/internal/distribute"
	"invariant/internal/has"
	"invariant/internal/identity"
	"invariant/internal/names"
	"invariant/internal/storage"
)

func main() {
	var dir string
	flag.StringVar(&dir, "dir", "", "Base directory for file system storage")
	var discoveryURL string
	flag.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	var advertiseAddr string
	flag.StringVar(&advertiseAddr, "advertise", "", "Address to advertise to the discovery service")
	var distributeArg string
	flag.StringVar(&distributeArg, "distribute", "", "ID or Name of the distribute service to register with")
	var hasIDs string
	flag.StringVar(&hasIDs, "has", "", "Comma-separated list of IDs implementing the Has protocol")
	var hasBatchSize int
	flag.IntVar(&hasBatchSize, "has-batch-size", 10000, "Number of block addresses to send per request")
	var hasBatchDuration time.Duration
	flag.DurationVar(&hasBatchDuration, "has-duration", 1*time.Second, "Maximum duration to wait before sending a batch of new block notifications")
	var port int
	flag.IntVar(&port, "port", 0, "Port to listen on (0 for random available port)")
	flag.Parse()

	var s storage.Storage
	if dir != "" {
		s = storage.NewFileSystemStorage(dir)
	} else {
		s = storage.NewInMemoryStorage()
	}

	server := storage.NewStorageServer(s)

	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", addr, err)
	}

	actualPort := listener.Addr().(*net.TCPAddr).Port

	if discoveryURL != "" {
		id := s.(identity.Provider).ID()
		client := discovery.NewClient(discoveryURL, nil)

		// Configure the storage server to use discovery for fetching
		server.WithDiscovery(client)

		err := discovery.AdvertiseAndRegister(client, id, advertiseAddr, actualPort, []string{"storage-v1"})
		if err != nil {
			log.Fatalf("Failed to register with discovery service: %v", err)
		}
		log.Printf("Registered with discovery service %s as %s", discoveryURL, id)
	}

	var hasClients []storage.HasClient
	if hasIDs != "" {
		if discoveryURL == "" {
			log.Fatalf("Discovery service is required to use the -has flag")
		}

		// Use a discovery client to resolve Has service IDs
		dClient := discovery.NewClient(discoveryURL, nil)

		ids := strings.Split(hasIDs, ",")
		for _, hid := range ids {
			hid = strings.TrimSpace(hid)
			if hid == "" {
				continue
			}

			desc, ok := dClient.Get(hid)
			if !ok {
				log.Printf("Warning: Could not find address for Has service ID %s", hid)
				continue
			}

			hasClients = append(hasClients, has.NewClient(desc.Address, nil))
		}
	}

	if distributeArg != "" {
		if discoveryURL == "" {
			log.Fatalf("Discovery service is required to use the -distribute flag")
		}

		dClient := discovery.NewClient(discoveryURL, nil)
		var distID string

		// If it's a 64-character hex string, it's an ID. Otherwise, resolve it via names service.
		if len(distributeArg) == 64 {
			distID = distributeArg
		} else {
			namesServers, err := dClient.Find("names-v1", 100)
			if err != nil || len(namesServers) == 0 {
				log.Fatalf("Warning: Could not find any names servers to resolve distribute name %v", err)
			}

			resolved := false
			for _, ns := range namesServers {
				nClient := names.NewClient(ns.Address, nil)
				entry, err := nClient.Get(distributeArg)
				if err == nil {
					distID = entry.Value
					resolved = true
					break
				}
			}
			if !resolved {
				log.Fatalf("Could not resolve distribute name %s using names servers", distributeArg)
			}
		}

		desc, ok := dClient.Get(distID)
		if !ok {
			log.Fatalf("Could not find distribute service %s in discovery", distID)
		}

		distClient := distribute.NewClient(desc.Address, nil)
		id := s.(identity.Provider).ID()
		if err := distClient.Register(id); err != nil {
			log.Fatalf("Failed to register with distribute service %s: %v", distID, err)
		}
		log.Printf("Registered with distribute service %s at %s", distID, desc.Address)

		hasClients = append(hasClients, has.NewClient(desc.Address, nil))
	}

	if len(hasClients) > 0 {
		server.StartHasNotification(hasClients, hasBatchSize, hasBatchDuration)
	}

	log.Printf("Listening on :%d...", actualPort)
	if dir != "" {
		log.Printf("Using File System storage at %s", dir)
	} else {
		log.Printf("Using In-Memory storage")
	}
	log.Fatal(http.Serve(listener, server))
}
