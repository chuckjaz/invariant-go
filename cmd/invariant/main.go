package main

import (
	"fmt"
	"os"

	"invariant/internal/config"
)

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: invariant <command> [options]\n\n")
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  start    Start invariant services from configuration\n")
	fmt.Fprintf(os.Stderr, "  slot     Allocate a new slot from the slots service\n")
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "start":
		runStart(cfg, os.Args[2:])
	case "slot":
		runSlot(cfg, os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command %q\n", os.Args[1])
		usage()
	}
}
