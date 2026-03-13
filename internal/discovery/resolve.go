package discovery

import (
	"context"
	"fmt"

	"invariant/internal/names"
)

// ResolveName takes an idOrName string and uses the discovery client to find "names-v1" services
// to resolve it if it's not a 64-character ID. It returns the 64-character ID.
func ResolveName(ctx context.Context, dClient Discovery, idOrName string) (string, error) {
	if len(idOrName) == 64 {
		return idOrName, nil
	}

	namesServers, err := dClient.Find(ctx, "names-v1", 100)
	if err == nil && len(namesServers) > 0 {
		for _, ns := range namesServers {
			nClient := names.NewClient(ns.Address, nil)
			entry, err := nClient.Get(ctx, idOrName)
			if err == nil {
				return entry.Value, nil
			}
		}
	}

	// Fallback to DNS
	dnsClient := names.NewDNSClient(nil)
	entry, err := dnsClient.Get(ctx, idOrName)
	if err == nil {
		return entry.Value, nil
	}

	return "", fmt.Errorf("could not resolve name %s using names servers or DNS", idOrName)
}

// Resolve uses ResolveName to find the ID and then looks up the service.
func Resolve(ctx context.Context, dClient Discovery, idOrName string) (ServiceDescription, error) {
	id, err := ResolveName(ctx, dClient, idOrName)
	if err != nil {
		return ServiceDescription{}, err
	}
	desc, ok := dClient.Get(ctx, id)
	if !ok {
		return ServiceDescription{}, fmt.Errorf("service %s not found", id)
	}
	return desc, nil
}
