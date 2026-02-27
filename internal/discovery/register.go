package discovery

import (
	"fmt"
	"net/url"

	"invariant/internal/names"
)

// AdvertiseAndRegister forms the complete advertise URL and registers the service
// with the discovery service. If the advertise address is empty, it uses localhost.
// If it lacks a port, the port is appended.
func AdvertiseAndRegister(disc Discovery, id, advertiseAddr string, port int, protocols []string) error {
	if advertiseAddr == "" {
		advertiseAddr = fmt.Sprintf("http://localhost:%d", port)
	} else {
		u, err := url.Parse(advertiseAddr)
		if err != nil {
			return fmt.Errorf("invalid advertise address: %v", err)
		}
		if u.Port() == "" {
			u.Host = fmt.Sprintf("%s:%d", u.Hostname(), port)
			advertiseAddr = u.String()
		}
	}

	return disc.Register(ServiceRegistration{
		ID:        id,
		Address:   advertiseAddr,
		Protocols: protocols,
	})
}

// RegisterName uses the discovery service to find a "names-v1" service
// and registers the given name for the given ID with the specified protocols.
func RegisterName(disc Discovery, name, id string, protocols []string) error {
	if disc == nil {
		return fmt.Errorf("a discovery service is required for the service to be named")
	}

	nameServices, err := disc.Find("names-v1", 1)
	if err != nil || len(nameServices) == 0 {
		return fmt.Errorf("a discovery service with a registered names service is required for the service to be named")
	}

	nameClient := names.NewClient(nameServices[0].Address, nil)
	return nameClient.Put(name, id, protocols)
}
