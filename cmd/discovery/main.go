package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"invariant/internal/discovery"
)

func main() {
	var port int
	flag.IntVar(&port, "port", 3003, "Port to listen on")
	var upstreamURL string
	flag.StringVar(&upstreamURL, "upstream", "", "Upstream discovery service URL to delegate queries to")
	flag.Parse()

	var d discovery.Discovery
	localD := discovery.NewInMemoryDiscovery()

	if upstreamURL != "" {
		parent := discovery.NewClient(upstreamURL, nil)
		d = discovery.NewUpstreamDiscovery(localD, parent)
		log.Printf("Using Upstream discovery delegation pointing to %s", upstreamURL)
	} else {
		d = localD
		log.Printf("Using standalone In-Memory discovery storage")
	}

	server := discovery.NewDiscoveryServer(d)

	addr := fmt.Sprintf(":%d", port)
	log.Printf("Discovery service listening on %s...", addr)

	log.Fatal(http.ListenAndServe(addr, server))
}
