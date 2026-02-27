// Package main provides the command-line utility for the slots service.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"invariant/internal/discovery"
	"invariant/internal/has"
	"invariant/internal/slots"
)

func generateID() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func main() {
	var id string
	flag.StringVar(&id, "id", "", "ID of the slots service (32-byte hex). Randomly generated if not provided.")
	var dir string
	flag.StringVar(&dir, "dir", "", "Base directory for file system slots storage")
	var discoveryURL string
	flag.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	var advertiseAddr string
	flag.StringVar(&advertiseAddr, "advertise", "", "Address to advertise to the discovery service")
	var port int
	flag.IntVar(&port, "port", 0, "Port to listen on (0 for random available port)")
	var snapshotInterval time.Duration
	flag.DurationVar(&snapshotInterval, "snapshot-interval", 1*time.Hour, "Interval between snapshots for file system storage")
	var hasIDs string
	flag.StringVar(&hasIDs, "has", "", "Comma-separated list of IDs implementing the Has protocol (e.g. finder)")
	var hasBatchSize int
	flag.IntVar(&hasBatchSize, "has-batch-size", 10000, "Number of slot IDs to send per request")
	var hasBatchDuration time.Duration
	flag.DurationVar(&hasBatchDuration, "has-duration", 1*time.Second, "Maximum duration to wait before sending a batch of new slot notifications")
	var name string
	flag.StringVar(&name, "name", "", "Name to register with the names service")
	flag.Parse()

	if id == "" {
		id = generateID()
	}

	var s slots.Slots
	if dir != "" {
		fss, err := slots.NewFileSystemSlots(dir, snapshotInterval)
		if err != nil {
			log.Fatalf("Failed to initialize file system slots: %v", err)
		}
		defer fss.Close()
		s = fss
	} else {
		s = slots.NewMemorySlots(id)
	}

	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", addr, err)
	}

	actualPort := listener.Addr().(*net.TCPAddr).Port

	var disc discovery.Discovery
	if discoveryURL != "" {
		disc = discovery.NewClient(discoveryURL, nil)

		err := discovery.AdvertiseAndRegister(disc, s.ID(), advertiseAddr, actualPort, []string{"slots-v1"})
		if err != nil {
			log.Fatalf("Failed to register with discovery service: %v", err)
		}
		log.Printf("Registered with discovery service %s as %s", discoveryURL, s.ID())
	}

	if name != "" {
		if disc == nil {
			log.Fatalf("Cannot register name without a valid discovery service")
		}
		err := discovery.RegisterName(disc, name, s.ID(), []string{"slots-v1"})
		if err != nil {
			log.Fatalf("Failed to register name %q: %v", name, err)
		}
		log.Printf("Registered name %q for ID %s", name, s.ID())
	}

	server := slots.NewServer(s)

	var hasClients []slots.HasClient
	if disc != nil {
		for hid := range strings.SplitSeq(hasIDs, ",") {
			hid = strings.TrimSpace(hid)
			if hid == "" {
				continue
			}

			// Resolve name to ID if it's not a hex ID
			resolvedID, err := discovery.ResolveName(disc, hid)
			if err != nil {
				log.Fatalf("Could not resolve has name/id %s: %v", hid, err)
				continue
			}

			desc, ok := disc.Get(resolvedID)
			if !ok {
				log.Fatalf("Could not find address for Has service %s", resolvedID)
				continue
			}

			hasClients = append(hasClients, has.NewClient(desc.Address, nil))
		}
	} else if hasIDs != "" {
		log.Fatalf("a discovery service is required to use the -has flag")
	}

	if len(hasClients) > 0 {
		server.StartHasNotification(hasClients, hasBatchSize, hasBatchDuration)
	}

	log.Printf("Slots service (ID %s) listening on :%d...", s.ID(), actualPort)
	if dir != "" {
		log.Printf("Using File System Slots storage at %s", dir)
	} else {
		log.Printf("Using In-Memory Slots storage")
	}

	log.Fatal(http.Serve(listener, server))
}
