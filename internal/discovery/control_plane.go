package discovery

import "context"

// ProtocolControlPlane is the protocol token for the Invariant Control Plane.
const ProtocolControlPlane = "itp-v1"

// ControlPlane manages client/node admission and WireGuard public key verification.
type ControlPlane interface {
	// RegisterClient registers a client's WireGuard public key and its identity.
	RegisterClient(ctx context.Context, publicKey string, id NodeIdentity) error

	// VerifyKey checks if a WireGuard public key is authorized and returns its associated identity.
	VerifyKey(ctx context.Context, publicKey string) (bool, *NodeIdentity, error)

	// AuthorizedKeys returns a list of all currently authorized WireGuard public keys.
	AuthorizedKeys(ctx context.Context) ([]string, error)
}
