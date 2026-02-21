package discovery

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
	Get(id string) (ServiceDescription, bool)
	Find(protocol string, count int) ([]ServiceDescription, error)
	Register(reg ServiceRegistration) error
}
