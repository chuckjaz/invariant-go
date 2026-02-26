package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"invariant/internal/discovery"
	"invariant/internal/distribute"
	"invariant/internal/names"
)

func main() {
	var id string
	flag.StringVar(&id, "id", "", "ID of the distribute service (32-byte hex). Randomly generated if not provided.")
	var discoveryURL string
	flag.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	var advertiseAddr string
	flag.StringVar(&advertiseAddr, "advertise", "", "Address to advertise to the discovery service")
	var repFactor int
	flag.IntVar(&repFactor, "N", 3, "Replication factor for blocks")
	var port int
	flag.IntVar(&port, "port", 0, "Port to listen on (0 for random available port)")
	var name string
	flag.StringVar(&name, "name", "", "Name to register with the names service")
	flag.Parse()

	var disc discovery.Discovery
	if discoveryURL != "" {
		disc = discovery.NewClient(discoveryURL, nil)
	}

	d := distribute.NewInMemoryDistribute(disc, repFactor, 3)
	if disc != nil {
		d.StartSync(10 * time.Second)
	}

	server := distribute.NewDistributeServer(id, d)

	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", addr, err)
	}

	actualPort := listener.Addr().(*net.TCPAddr).Port

	if discoveryURL != "" {
		err := discovery.AdvertiseAndRegister(disc, server.ID(), advertiseAddr, actualPort, []string{"distribute-v1", "has-v1"})
		if err != nil {
			log.Fatalf("Failed to register with discovery service: %v", err)
		}
		log.Printf("Registered with discovery service %s as %s", discoveryURL, server.ID())
	}

	if name != "" {
		if disc == nil {
			log.Fatalf("a discovery service with a registered names service is required for the service to be named.")
		}
		nameServices, err := disc.Find("names-v1", 1)
		if err != nil || len(nameServices) == 0 {
			log.Fatalf("a discovery service with a registered names service is required for the service to be named.")
		}

		nameClient := names.NewClient(nameServices[0].Address, nil)
		err = nameClient.Put(name, server.ID(), []string{"distribute-v1", "has-v1"})
		if err != nil {
			log.Fatalf("Failed to register name with names service at %s: %v", nameServices[0].Address, err)
		}
		log.Printf("Registered name %q for ID %s", name, server.ID())
	}

	log.Printf("Distribute service (ID %s) listening on :%d...", server.ID(), actualPort)
	log.Printf("Using In-Memory distribute storage")

	log.Fatal(http.Serve(listener, server))
}
