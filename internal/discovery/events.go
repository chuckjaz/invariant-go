package discovery

import (
	"context"
	"time"
)

// DiscoveryEventKind identifies the mutation event type.
type DiscoveryEventKind string

const (
	EventRegisterService   DiscoveryEventKind = "register_service"
	EventUnregisterService DiscoveryEventKind = "unregister_service"
	EventRegisterClient    DiscoveryEventKind = "register_client"
	EventCreateServiceKey  DiscoveryEventKind = "create_service_key"
	EventRevokeServiceKey  DiscoveryEventKind = "revoke_service_key"
)

// DiscoveryEvent represents an immutable state mutation in Discovery.
type DiscoveryEvent struct {
	Kind        DiscoveryEventKind   `json:"kind"`
	Service     *ServiceRegistration `json:"service,omitempty"`
	ServiceID   string               `json:"service_id,omitempty"`
	ClientKey   string               `json:"client_key,omitempty"`
	ClientIdent *NodeIdentity        `json:"client_ident,omitempty"`
	ServiceKey  *ServiceKeyRecord    `json:"service_key,omitempty"`
	RevokeKey   string               `json:"revoke_key,omitempty"`
	RevokedBy   *NodeIdentity        `json:"revoked_by,omitempty"`
	Timestamp   time.Time            `json:"timestamp"`
}

// EventSink receives state mutation events for replication, journaling, or backup.
type EventSink interface {
	Append(ctx context.Context, event DiscoveryEvent) error
	Flush(ctx context.Context) error
	Close() error
}
