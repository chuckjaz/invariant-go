package main

import (
	"flag"
	"fmt"
	"os"

	"invariant/internal/config"
	"invariant/internal/discovery"
)

func runLookup(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("lookup", flag.ExitOnError)
	var discoveryURL string
	fs.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: invariant lookup [options] <name>\n")
		fmt.Fprintf(os.Stderr, "Looks up a name in the names service and prints the resolved address or ID to stdout.\n\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if len(fs.Args()) < 1 {
		fmt.Fprintf(os.Stderr, "Error: missing name to lookup\n")
		fs.Usage()
		os.Exit(1)
	}

	name := fs.Args()[0]

	if discoveryURL == "" && globalCfg != nil {
		discoveryURL = globalCfg.Discovery
	}

	if discoveryURL == "" {
		fmt.Fprintf(os.Stderr, "Discovery service URL is not configured. Please provide it via --discovery or configuration file.\n")
		os.Exit(1)
	}

	dClient := discovery.NewClient(discoveryURL, nil)

	resolved, err := discovery.ResolveName(dClient, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to resolve name %q: %v\n", name, err)
		os.Exit(1)
	}

	fmt.Println(resolved)
}
