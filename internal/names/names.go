package names

import "errors"

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
	Get(name string) (NameEntry, error)
	Put(name string, value string, tokens []string) error
	Delete(name string, expectedValue string) error
}
