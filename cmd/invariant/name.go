package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"

	"invariant/internal/config"
	"invariant/internal/discovery"
	"invariant/internal/names"
)

func runName(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("name", flag.ExitOnError)
	var tokensStr string
	fs.StringVar(&tokensStr, "tokens", "", "Comma separated list of protocol version tokens")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: invariant name <name> <32-byte-hex-value>\n")
		fmt.Fprintf(os.Stderr, "Registers a given name to the provided 32-byte hex value using the names service.\n\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if len(fs.Args()) < 2 {
		fmt.Fprintf(os.Stderr, "Error: missing name or hex value\n")
		fs.Usage()
		os.Exit(1)
	}

	name := fs.Args()[0]
	hexValue := fs.Args()[1]

	if len(hexValue) != 64 {
		fmt.Fprintf(os.Stderr, "Error: value must be a 64-character (32-byte) hex string\n")
		os.Exit(1)
	}

	if _, err := hex.DecodeString(hexValue); err != nil {
		fmt.Fprintf(os.Stderr, "Error: value is not valid hex: %v\n", err)
		os.Exit(1)
	}

	if globalCfg == nil || globalCfg.Discovery == "" {
		fmt.Fprintf(os.Stderr, "Discovery service URL is not configured. Please ensure ~/.invariant/config.yaml is valid with a discovery URL.\n")
		os.Exit(1)
	}

	dClient := discovery.NewClient(globalCfg.Discovery, nil)

	// find names service
	id, err := dClient.Find("names-v1", 1)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not query discovery service for names-v1: %v\n", err)
		os.Exit(1)
	}
	if len(id) == 0 {
		fmt.Fprintf(os.Stderr, "Could not find any names-v1 service\n")
		os.Exit(1)
	}

	namesClient := names.NewClient(id[0].Address, nil)

	var tokens []string
	if tokensStr != "" {
		tokens = strings.Split(tokensStr, ",")
	}

	err = namesClient.Put(name, hexValue, tokens)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to register name: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Successfully registered name %q to %s\n", name, hexValue)
}
