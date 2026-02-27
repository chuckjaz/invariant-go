package discovery

import (
	"fmt"

	"invariant/internal/names"
)

// ResolveName takes an idOrName string and uses the discovery client to find "names-v1" services
// to resolve it if it's not a 64-character ID. It returns the 64-character ID.
func ResolveName(dClient Discovery, idOrName string) (string, error) {
	if len(idOrName) == 64 {
		return idOrName, nil
	}

	namesServers, err := dClient.Find("names-v1", 100)
	if err == nil && len(namesServers) > 0 {
		for _, ns := range namesServers {
			nClient := names.NewClient(ns.Address, nil)
			entry, err := nClient.Get(idOrName)
			if err == nil {
				return entry.Value, nil
			}
		}
	}

	// Fallback to DNS
	dnsClient := names.NewDNSClient(nil)
	entry, err := dnsClient.Get(idOrName)
	if err == nil {
		return entry.Value, nil
	}

	return "", fmt.Errorf("could not resolve name %s using names servers or DNS", idOrName)
}
