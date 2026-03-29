package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"invariant/internal/config"
	"invariant/internal/nfs"
)

func runNfs(globalCfg *config.InvariantConfig, args []string) {
	fsFlags := flag.NewFlagSet("nfs", flag.ExitOnError)
	var listenAddr string
	fsFlags.StringVar(&listenAddr, "listen", ":2049", "Address to listen for NFS requests on")

	var commonFlags CommonMountFlags
	commonFlags.Register(fsFlags)

	fsFlags.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: invariant nfs [options]\n\n")
		fsFlags.PrintDefaults()
	}
	fsFlags.Parse(args)

	filesrv := SetupFileSystem(globalCfg, &commonFlags)
	defer filesrv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server, err := nfs.Start(ctx, listenAddr, filesrv, 1) // 1 is root
	if err != nil {
		log.Fatalf("Failed to start NFS server: %v", err)
	}
	defer server.Close()

	log.Printf("NFS server listening on %s", listenAddr)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down NFS server...")
}
