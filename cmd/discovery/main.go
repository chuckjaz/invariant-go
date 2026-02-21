package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"invariant/internal/discovery"
)

func main() {
	d := discovery.NewInMemoryDiscovery()
	server := discovery.NewDiscoveryServer(d)

	port := os.Getenv("PORT")
	if port == "" {
		port = "3003"
	}

	addr := fmt.Sprintf(":%s", port)
	log.Printf("Discovery service listening on %s...", addr)
	log.Printf("Using In-Memory discovery storage")

	log.Fatal(http.ListenAndServe(addr, server))
}
