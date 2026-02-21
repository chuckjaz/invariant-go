package identity

// Provider is an interface for types that can provide an ID
type Provider interface {
	ID() string
}
