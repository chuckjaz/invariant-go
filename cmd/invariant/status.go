package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"text/tabwriter"
	"time"

	"invariant/internal/config"
	"invariant/internal/discovery"
	"invariant/internal/identity"
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
		desc   discovery.ServiceDescription
		status string
	}

	results := make([]result, len(descs))
	var wg sync.WaitGroup

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

			results[i] = result{
				desc:   d,
				status: status,
			}
		}(i, d)
	}

	wg.Wait()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "Registered ID\tTarget Address\tStatus")
	for _, r := range results {
		fmt.Fprintf(w, "%s\t%s\t%s\n", r.desc.ID, r.desc.Address, r.status)
	}
	w.Flush()
}
