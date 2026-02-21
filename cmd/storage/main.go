package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"invariant/internal/storage"
)

func main() {
	var dir string
	flag.StringVar(&dir, "dir", "", "Base directory for file system storage")
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
	log.Printf("Listening on %s...", addr)
	if dir != "" {
		log.Printf("Using File System storage at %s", dir)
	} else {
		log.Printf("Using In-Memory storage")
	}
	log.Fatal(http.ListenAndServe(addr, server))
}
