package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"invariant/internal/config"
	"invariant/internal/discovery"
	"invariant/internal/slots"
)

func runSlot(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("slot", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: invariant slot\n")
		fmt.Fprintf(os.Stderr, "Allocates a new slot using the discovery service.\n\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if globalCfg == nil || globalCfg.Discovery == "" {
		fmt.Fprintf(os.Stderr, "Discovery service URL is not configured. Please ensure ~/.invariant is valid with a discovery URL.\n")
		os.Exit(1)
	}

	dClient := discovery.NewClient(globalCfg.Discovery, nil)

	// find slots service
	id, err := dClient.Find("slots-v1", 1)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not query discovery service: %v\n", err)
		os.Exit(1)
	}
	if len(id) == 0 {
		fmt.Fprintf(os.Stderr, "Could not find any slots-v1 service\n")
		os.Exit(1)
	}

	slotsClient := slots.NewClient(id[0].Address, nil)

	b := make([]byte, 32)
	rand.Read(b)
	slotID := hex.EncodeToString(b)

	err = slotsClient.Create(slotID, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to allocate slot: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Allocated new slot: %s\n", slotID)
}
