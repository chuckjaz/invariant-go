package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"invariant/internal/discovery"
	"invariant/internal/finder"
	"invariant/internal/tags"
	"invariant/internal/trace"
)

func generateID() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func main() {
	var id string
	flag.StringVar(&id, "id", "", "ID of the finder service (32-byte hex). Ramdomly generated if not provided.")
	var dir string
	flag.StringVar(&dir, "dir", "", "Directory for persisting finder state (e.g. ID)")
	var discoveryURL string
	flag.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	var advertiseAddr string
	flag.StringVar(&advertiseAddr, "advertise", "", "Address to advertise to the discovery service")
	tagFlags := tags.RegisterFlags()
	var port int
	flag.IntVar(&port, "port", 3004, "Port to listen on (using 3004 to not conflict with storage/discovery)")
	var name string
	flag.StringVar(&name, "name", "", "Name to register with the names service")
	var enableTrace bool
	flag.BoolVar(&enableTrace, "trace", false, "Enable distributed tracing on this service")
	flag.Parse()

	if id == "" {
		if dir != "" {
			os.MkdirAll(dir, 0755)
			idPath := filepath.Join(dir, "id")
			if data, err := os.ReadFile(idPath); err == nil && len(data) == 64 {
				id = string(data)
			} else {
				id = generateID()
				if err := os.WriteFile(idPath, []byte(id), 0644); err != nil {
					log.Printf("Failed to write ID file: %v", err)
				}
			}
		} else {
			id = generateID()
		}
	}

	f, err := finder.NewMemoryFinder(id)
	if err != nil {
		log.Fatalf("Failed to create finder: %v", err)
	}

	addr := fmt.Sprintf(":%d", port)

	var disc discovery.Discovery
	if discoveryURL != "" {
		disc = discovery.NewClient(discoveryURL, nil)

		err := discovery.AdvertiseAndRegister(context.Background(), disc, id, advertiseAddr, port, []string{"finder-v1", "notify-v1"}, tagFlags.Tags())
		if err != nil {
			log.Fatalf("Failed to register with discovery service: %v", err)
		}
		log.Printf("Registered with discovery service %s as %s", discoveryURL, id)
	}

	if name != "" {
		go func() {
			err := discovery.RegisterName(context.Background(), disc, name, id, []string{"finder-v1", "notify-v1"})
			if err != nil {
				log.Printf("Failed to register name %q: %v", name, err)
			} else {
				log.Printf("Registered name %q for ID %s", name, id)
			}
		}()
	}

	server := finder.NewFinderServer(f, disc)
	if enableTrace {
		server.WithTracer(trace.NewTracer(10000))
	}

	log.Printf("Finder service (ID %s) listening on %s...", id, addr)
	log.Printf("Using In-Memory routing and storage mapping")

	log.Fatal(http.ListenAndServe(addr, server))
}
