package names

import (
	"context"
	"errors"
)

var (
	ErrNotFound           = errors.New("name not found")
	ErrPreconditionFailed = errors.New("precondition failed")
)

// NameEntry represents the data stored for a name
type NameEntry struct {
	Value  string   `json:"value"`
	Tokens []string `json:"tokens"`
}

// Names defines the interface for the names service
type Names interface {
	Get(ctx context.Context, name string) (NameEntry, error)
	Put(ctx context.Context, name string, value string, tokens []string) error
	Delete(ctx context.Context, name string, expectedValue string) error
}
