package container

// Container represents an interface for tracking blocks held by storage nodes.
type Container interface {
	Has(id string, addresses []string) error
}
