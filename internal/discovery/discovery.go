package discovery

import "context"

// NodeIdentity captures user or service identity.
type NodeIdentity struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

// ServiceDescription describes a registered service.
type ServiceDescription struct {
	ID        string        `json:"id"`
	Address   string        `json:"address"`
	Protocols []string      `json:"protocols"`
	Tags      []string      `json:"tags,omitempty"`
	PublicKey string        `json:"public_key,omitempty"`
	Identity  *NodeIdentity `json:"identity,omitempty"`
}

// ServiceRegistration is the payload used to register a service.
type ServiceRegistration struct {
	ID        string        `json:"id"`
	Address   string        `json:"address"`
	Protocols []string      `json:"protocols"`
	Tags      []string      `json:"tags,omitempty"`
	PublicKey string        `json:"public_key,omitempty"`
	Identity  *NodeIdentity `json:"identity,omitempty"`
}

// Discovery dictates the necessary requirements for the discovery service.
type Discovery interface {
	Get(ctx context.Context, id string) (ServiceDescription, bool)
	Find(ctx context.Context, protocol, tag string, count int) ([]ServiceDescription, error)
	Register(ctx context.Context, reg ServiceRegistration) error
}
