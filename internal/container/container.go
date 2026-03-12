package container

import "context"

// Container represents an interface for tracking blocks held by storage nodes.
type Container interface {
	Notify(ctx context.Context, id string, addresses []string) error
}
