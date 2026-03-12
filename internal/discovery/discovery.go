package discovery

import "context"

// ServiceDescription describes a registered service.
type ServiceDescription struct {
	ID        string   `json:"id"`
	Address   string   `json:"address"`
	Protocols []string `json:"protocols"`
}

// ServiceRegistration is the payload used to register a service.
type ServiceRegistration struct {
	ID        string   `json:"id"`
	Address   string   `json:"address"`
	Protocols []string `json:"protocols"`
}

// Discovery dictates the necessary requirements for the discovery service.
type Discovery interface {
	Get(ctx context.Context, id string) (ServiceDescription, bool)
	Find(ctx context.Context, protocol string, count int) ([]ServiceDescription, error)
	Register(ctx context.Context, reg ServiceRegistration) error
}
