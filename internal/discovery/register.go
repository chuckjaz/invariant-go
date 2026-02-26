package discovery

import (
	"fmt"
	"net/url"
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
