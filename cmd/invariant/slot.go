package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"invariant/internal/config"
	"invariant/internal/discovery"
	"invariant/internal/finder"
	"invariant/internal/names"
	"invariant/internal/slots"
)

func runSlot(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("slot", flag.ExitOnError)
	nameFlag := fs.String("name", "", "Optional name to register the newly allocated slot with")
	protectedFlag := fs.Bool("protected", false, "Generate an Ed25519 256-bit elliptic curve key pair. The 32-byte public key becomes the slot ID, and the private key is saved in ~/.invariant/keys")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: invariant slot [options] <block-address>\n")
		fmt.Fprintf(os.Stderr, "Allocates a new slot using the discovery service and the given initial block address.\n\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if len(fs.Args()) < 1 {
		fmt.Fprintf(os.Stderr, "Error: missing block address\n")
		fs.Usage()
		os.Exit(1)
	}

	blockAddress := fs.Args()[0]

	if len(blockAddress) != 64 {
		fmt.Fprintf(os.Stderr, "Error: block address must be a 64-character (32-byte) hex string\n")
		os.Exit(1)
	}

	if _, err := hex.DecodeString(blockAddress); err != nil {
		fmt.Fprintf(os.Stderr, "Error: block address is not valid hex: %v\n", err)
		os.Exit(1)
	}

	if globalCfg == nil || globalCfg.Discovery == "" {
		fmt.Fprintf(os.Stderr, "Discovery service URL is not configured. Please ensure ~/.invariant/config.yaml is valid with a discovery URL.\n")
		os.Exit(1)
	}

	dClient := discovery.NewClient(globalCfg.Discovery, nil)

	// verify with finder
	finderID, err := dClient.Find("finder-v1", 1)
	if err == nil && len(finderID) > 0 {
		fClient := finder.NewClient(finderID[0].Address, nil)
		res, err := fClient.Find(blockAddress)
		if err != nil || len(res) == 0 {
			fmt.Fprintf(os.Stderr, "Warning: Block address %s could not be found via finder service.\n", blockAddress)
		}
	}

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

	var slotID string
	var privKey ed25519.PrivateKey

	if *protectedFlag {
		fmt.Println("Generating protected slot using Ed25519 (256-bit elliptic curve)...")
		pub, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to generate key pair: %v\n", err)
			os.Exit(1)
		}
		slotID = hex.EncodeToString(pub)
		privKey = priv
	} else {
		b := make([]byte, 32)
		rand.Read(b)
		slotID = hex.EncodeToString(b)
	}
	err = slotsClient.Create(slotID, blockAddress)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to allocate slot: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Allocated new slot: %s\n", slotID)

	if *protectedFlag {
		keysDir, err := config.KeysDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to locate keys directory: %v\n", err)
		} else {
			keyPath := filepath.Join(keysDir, fmt.Sprintf("%s.key", slotID))
			err = os.WriteFile(keyPath, privKey, 0600)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Failed to save private key to %s: %v\n", keyPath, err)
			} else {
				fmt.Printf("Private key securely saved to: %s\n", keyPath)
			}
		}
	}

	if *nameFlag != "" {
		namesID, err := dClient.Find("names-v1", 1)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not query discovery service for names-v1: %v\n", err)
			os.Exit(1)
		}
		if len(namesID) == 0 {
			fmt.Fprintf(os.Stderr, "Warning: Could not find any names-v1 service to register name.\n")
		} else {
			namesClient := names.NewClient(namesID[0].Address, nil)
			err = namesClient.Put(*nameFlag, slotID, []string{"slot-v1"})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to register name: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Successfully registered name %q to slot %s\n", *nameFlag, slotID)
		}
	}
}
