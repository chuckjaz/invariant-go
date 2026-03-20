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
	fmt.Fprintf(os.Stderr, "  name     Register a name to a 32-byte hex value\n")
	fmt.Fprintf(os.Stderr, "  lookup   Lookup a name and print the resolved address\n")
	fmt.Fprintf(os.Stderr, "  mount    Mount the invariant file system using FUSE\n")
	fmt.Fprintf(os.Stderr, "  nfs      Start the invariant file system as an NFS Server\n")
	fmt.Fprintf(os.Stderr, "  upload   Upload a local directory as a file tree\n")
	fmt.Fprintf(os.Stderr, "  print    Print a block's contents to standard output\n")
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
	case "name":
		runName(cfg, os.Args[2:])
	case "lookup":
		runLookup(cfg, os.Args[2:])
	case "mount":
		runMount(cfg, os.Args[2:])
	case "nfs":
		runNfs(cfg, os.Args[2:])
	case "upload":
		runUpload(cfg, os.Args[2:])
	case "print":
		runPrint(cfg, os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command %q\n", os.Args[1])
		usage()
	}
}
