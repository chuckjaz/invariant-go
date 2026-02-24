package names

import (
	"context"
	"errors"
	"net"
	"strings"
)

var (
	ErrNotSupported = errors.New("operation not supported")
)

// Resolver defines the interface for DNS TXT record lookups
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// DNSClient implements the Names interface using DNS TXT records
type DNSClient struct {
	resolver Resolver
}

// NewDNSClient creates a new DNSClient with an optional custom resolver.
// If resolver is nil, the default net.DefaultResolver is used.
func NewDNSClient(resolver Resolver) *DNSClient {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &DNSClient{
		resolver: resolver,
	}
}

// Get retrieves the NameEntry for a given name using DNS TXT records.
// It looks for a TXT record with the prefix "invariant:"
func (c *DNSClient) Get(name string) (NameEntry, error) {
	txts, err := c.resolver.LookupTXT(context.Background(), name)
	if err != nil {
		// Differentiate between generic lookup errors and not found if possible.
		// For simplicity, we'll try to map common error cases or just return ErrNotFound
		// if no valid records are found after filtering.
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return NameEntry{}, ErrNotFound
		}
		// If it's another error, we could return it or still treat it as not found for our purposes
		// but let's see if we get any TXT records at all.
	}

	for _, txt := range txts {
		if strings.HasPrefix(txt, "invariant:") {
			content := strings.TrimPrefix(txt, "invariant:")
			parts := strings.SplitN(content, ";", 2)

			if len(parts) == 0 || parts[0] == "" {
				continue // invalid format
			}

			value := parts[0]
			var tokens []string
			if len(parts) == 2 && parts[1] != "" {
				tokens = strings.Split(parts[1], ",")
			} else {
				tokens = []string{}
			}

			return NameEntry{
				Value:  value,
				Tokens: tokens,
			}, nil
		}
	}

	return NameEntry{}, ErrNotFound
}

// Put is not supported by the DNS client
func (c *DNSClient) Put(name string, value string, tokens []string) error {
	return ErrNotSupported
}

// Delete is not supported by the DNS client
func (c *DNSClient) Delete(name string, expectedValue string) error {
	return ErrNotSupported
}
