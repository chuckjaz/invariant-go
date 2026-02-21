package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"invariant/internal/storage"
)

func main() {
	s := storage.NewInMemoryStorage()
	server := storage.NewStorageServer(s)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	addr := fmt.Sprintf(":%s", port)
	log.Printf("Listening on %s...", addr)
	log.Fatal(http.ListenAndServe(addr, server))
}
