package identity

// Identity is an interface for types that can provide an ID
type Identity interface {
	ID() string
}
