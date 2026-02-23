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
	flag.Parse()

	d := discovery.NewInMemoryDiscovery()
	server := discovery.NewDiscoveryServer(d)

	addr := fmt.Sprintf(":%d", port)
	log.Printf("Discovery service listening on %s...", addr)
	log.Printf("Using In-Memory discovery storage")

	log.Fatal(http.ListenAndServe(addr, server))
}
