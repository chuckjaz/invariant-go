package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/chuckjaz/invariant-go/internal/config"
	"github.com/chuckjaz/invariant-go/internal/discovery"
	"github.com/chuckjaz/invariant-go/internal/identity"
	"github.com/chuckjaz/invariant-go/internal/names"
)

func formatStatusAddress(addr string, verbose bool) string {
	if !verbose && (strings.HasPrefix(addr, "itp://") || strings.HasPrefix(addr, "tailcat://")) {
		return "<itp>"
	}
	return addr
}

func runStatus(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	var discoveryURL string
	fs.StringVar(&discoveryURL, "discovery", "", "Discovery service URL")
	var verbose bool
	fs.BoolVar(&verbose, "verbose", false, "Show full target addresses")
	_ = fs.Parse(args)

	if discoveryURL == "" && globalCfg != nil {
		discoveryURL = globalCfg.Discovery
	}
	if discoveryURL == "" {
		fatalf("Discovery service address is not configured")
	}

	client := newDiscoveryClient(discoveryURL, globalCfg)
	httpClient := newHTTPClient(globalCfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// protocol="" and tag="" retrieves all services
	descs, err := client.Find(ctx, "", "", 10000)
	if err != nil {
		fatalf("Failed to query discovery service: %v", err)
	}

	if len(descs) == 0 {
		fmt.Println("No services registered in Discovery.")
		return
	}

	type result struct {
		desc        discovery.ServiceDescription
		status      string
		mappedNames []string
	}

	results := make([]result, len(descs))
	var wg sync.WaitGroup

	var nameClients []names.Names
	for _, d := range descs {
		if slices.Contains(d.Protocols, "names-v1") {
			nameClients = append(nameClients, names.NewClient(d.Address, httpClient))
		}
	}

	for i, d := range descs {
		wg.Add(1)
		go func(i int, d discovery.ServiceDescription) {
			defer wg.Done()

			idClient := identity.NewClient(d.Address, httpClient)
			actualID := idClient.ID()

			status := "OFFLINE / ERROR"
			if actualID != "" {
				if actualID == d.ID {
					status = "HEALTHY"
				} else {
					status = fmt.Sprintf("MISMATCH (Got: %s)", actualID)
				}
			}

			var mappedNames []string
			if actualID != "" {
				seenNames := make(map[string]bool)
				for _, nc := range nameClients {
					if res, err := nc.Lookup(ctx, actualID); err == nil {
						for _, n := range res {
							if !seenNames[n] {
								seenNames[n] = true
								mappedNames = append(mappedNames, n)
							}
						}
					}
				}
			}

			results[i] = result{
				desc:        d,
				status:      status,
				mappedNames: mappedNames,
			}
		}(i, d)
	}

	wg.Wait()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "Registered ID\tTarget Address\tProtocols\tTags\tAliases\tStatus")
	for _, r := range results {
		protos := strings.Join(r.desc.Protocols, ", ")
		tags := strings.Join(r.desc.Tags, ", ")
		if tags == "" {
			tags = "-"
		}
		aliases := strings.Join(r.mappedNames, ", ")
		if aliases == "" {
			aliases = "-"
		}
		targetAddr := formatStatusAddress(r.desc.Address, verbose)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.desc.ID, targetAddr, protos, tags, aliases, r.status)
	}
	w.Flush()
}
