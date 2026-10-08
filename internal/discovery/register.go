package discovery

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"invariant/internal/names"
)

// RegistrationOption configures optional fields in AdvertiseAndRegister.
type RegistrationOption func(*ServiceRegistration)

// WithPublicKey sets the WireGuard public key for the service registration.
func WithPublicKey(key string) RegistrationOption {
	return func(r *ServiceRegistration) {
		r.PublicKey = key
	}
}

// WithIdentity sets the node identity for the service registration.
func WithIdentity(id NodeIdentity) RegistrationOption {
	return func(r *ServiceRegistration) {
		r.Identity = &id
	}
}

// AdvertiseAndRegister forms the complete advertise URL and registers the service
// with the discovery service. If the advertise address is empty, it uses localhost.
// If it lacks a port, the port is appended.
func AdvertiseAndRegister(ctx context.Context, disc Discovery, id, advertiseAddr string, port int, protocols, tags []string, opts ...RegistrationOption) error {
	if strings.HasPrefix(advertiseAddr, "itp://") || strings.HasPrefix(advertiseAddr, "tailcat://") {
		// ITP address, preserve as is
	} else if advertiseAddr == "" {
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

	reg := ServiceRegistration{
		ID:        id,
		Address:   advertiseAddr,
		Protocols: protocols,
		Tags:      tags,
	}
	for _, opt := range opts {
		opt(&reg)
	}

	return disc.Register(ctx, reg)
}

// RegisterName uses the discovery service to find a "names-v1" service
// and registers the given name for the given ID with the specified protocols.
func RegisterName(ctx context.Context, disc Discovery, name, id string, protocols []string) error {
	if disc == nil {
		return fmt.Errorf("a discovery service is required for the service to be named")
	}

	var lastErr error
	for range 5 {
		nameServices, err := disc.Find(ctx, "names-v1", "", 1)
		if err == nil && len(nameServices) > 0 {
			nameClient := names.NewClient(nameServices[0].Address, nil)
			err = nameClient.Put(ctx, name, id, protocols)
			if err == nil {
				return nil
			}
			lastErr = err
		} else {
			if err != nil {
				lastErr = err
			} else {
				lastErr = fmt.Errorf("no names service found")
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}

	return fmt.Errorf("failed to register name after 5 attempts: %v", lastErr)
}
