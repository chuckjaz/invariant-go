package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"invariant/internal/config"
	"invariant/internal/discovery"
	"invariant/internal/identity"
	"invariant/internal/names"
)

func runStatus(globalCfg *config.InvariantConfig, args []string) {
	if globalCfg.Discovery == "" {
		log.Fatalf("Discovery service address is not configured in ~/.invariant/config.yaml")
	}

	client := discovery.NewClient(globalCfg.Discovery, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// protocol="" retrieves all services
	descs, err := client.Find(ctx, "", 10000)
	if err != nil {
		log.Fatalf("Failed to query discovery service: %v", err)
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
		for _, p := range d.Protocols {
			if p == "names-v1" {
				nameClients = append(nameClients, names.NewClient(d.Address, nil))
				break
			}
		}
	}

	for i, d := range descs {
		wg.Add(1)
		go func(i int, d discovery.ServiceDescription) {
			defer wg.Done()

			idClient := identity.NewClient(d.Address, nil)
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
	fmt.Fprintln(w, "Registered ID\tTarget Address\tProtocols\tAliases\tStatus")
	for _, r := range results {
		protos := strings.Join(r.desc.Protocols, ", ")
		aliases := strings.Join(r.mappedNames, ", ")
		if aliases == "" {
			aliases = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.desc.ID, r.desc.Address, protos, aliases, r.status)
	}
	w.Flush()
}
