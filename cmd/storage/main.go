package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"invariant/internal/discovery"
	"invariant/internal/identity"
	"invariant/internal/storage"
)

func main() {
	var dir string
	flag.StringVar(&dir, "dir", "", "Base directory for file system storage")
	var discoveryURL string
	flag.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	var advertiseAddr string
	flag.StringVar(&advertiseAddr, "advertise", "", "Address to advertise to the discovery service")
	flag.Parse()

	var s storage.Storage
	if dir != "" {
		s = storage.NewFileSystemStorage(dir)
	} else {
		s = storage.NewInMemoryStorage()
	}

	server := storage.NewStorageServer(s)

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	addr := fmt.Sprintf(":%s", port)

	if discoveryURL != "" {
		if advertiseAddr == "" {
			advertiseAddr = fmt.Sprintf("http://localhost:%s", port)
		}

		id := s.(identity.Provider).ID()
		client := discovery.NewClient(discoveryURL, nil)

		// Configure the storage server to use discovery for fetching
		server.WithDiscovery(client)

		err := client.Register(discovery.ServiceRegistration{
			ID:        id,
			Address:   advertiseAddr,
			Protocols: []string{"storage-v1"},
		})
		if err != nil {
			log.Fatalf("Failed to register with discovery service: %v", err)
		}
		log.Printf("Registered with discovery service %s as %s", discoveryURL, id)
	}

	log.Printf("Listening on %s...", addr)
	if dir != "" {
		log.Printf("Using File System storage at %s", dir)
	} else {
		log.Printf("Using In-Memory storage")
	}
	log.Fatal(http.ListenAndServe(addr, server))
}
