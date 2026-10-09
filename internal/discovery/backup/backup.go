package backup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"invariant/internal/content"
	"invariant/internal/discovery"
	chainedjournal "invariant/internal/journal/chained"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// Assert that ChainedBackup implements discovery.EventSink
var _ discovery.EventSink = (*ChainedBackup)(nil)

// BackupOptions configures periodic CAS storage and slot backup for discovery journals.
type BackupOptions struct {
	BaseDir       string
	Storage       storage.Storage
	Slots         slots.Slots
	SlotID        string
	SlotAuth      []byte
	MaxEntries    int
	FlushInterval time.Duration
	WriterOpts    content.WriterOptions
}

// ChainedBackup wraps a ChainedJournal of DiscoveryEvents, backing up to CAS storage
// and updating a slot.
type ChainedBackup struct {
	journal *chainedjournal.ChainedJournal[discovery.DiscoveryEvent]
}

// NewChainedBackup creates a new ChainedBackup event sink.
func NewChainedBackup(opts BackupOptions) (*ChainedBackup, error) {
	cj, err := chainedjournal.NewChainedJournal[discovery.DiscoveryEvent](chainedjournal.ChainedJournalOptions{
		BaseDir:    opts.BaseDir,
		Storage:    opts.Storage,
		Slots:      opts.Slots,
		SlotID:     opts.SlotID,
		SlotAuth:   opts.SlotAuth,
		MaxEntries: opts.MaxEntries,
		WriterOpts: opts.WriterOpts,
	})
	if err != nil {
		return nil, err
	}
	return &ChainedBackup{journal: cj}, nil
}

// Append records a discovery mutation event into the local write-ahead log.
func (b *ChainedBackup) Append(ctx context.Context, event discovery.DiscoveryEvent) error {
	_, err := b.journal.Append(ctx, event, true)
	return err
}

// Flush forces rotation, CAS upload, and slot update.
func (b *ChainedBackup) Flush(ctx context.Context) error {
	return b.journal.Flush(ctx)
}

// Close flushes any pending entries and closes the journal.
func (b *ChainedBackup) Close() error {
	return b.journal.Close()
}

// ReplayTarget defines the interface required to replay discovery state.
type ReplayTarget interface {
	Register(ctx context.Context, reg discovery.ServiceRegistration) error
	RegisterClient(ctx context.Context, publicKey string, id discovery.NodeIdentity) error
	RevokeServiceKey(ctx context.Context, keyOrID string, revoker discovery.NodeIdentity) error
}

// Replay reads all historical events from CAS storage via slot and unflushed local files,
// applying them in chronological order to restore state.
func (b *ChainedBackup) Replay(ctx context.Context, target ReplayTarget) error {
	var allEntries []discovery.DiscoveryEvent

	remoteEntries, err := b.journal.LoadRemoteEntries(ctx, nil)
	if err == nil {
		allEntries = append(allEntries, remoteEntries...)
	}

	localEntries, err := b.journal.LoadLocalEntries()
	if err == nil {
		allEntries = append(allEntries, localEntries...)
	}

	for _, event := range allEntries {
		switch event.Kind {
		case discovery.EventRegisterService:
			if event.Service != nil {
				if err := target.Register(ctx, *event.Service); err != nil {
					return fmt.Errorf("failed to replay service registration: %w", err)
				}
			}
		case discovery.EventRegisterClient:
			if event.ClientIdent != nil {
				if err := target.RegisterClient(ctx, event.ClientKey, *event.ClientIdent); err != nil {
					return fmt.Errorf("failed to replay client registration: %w", err)
				}
			}
		case discovery.EventCreateServiceKey:
			if event.ServiceKey != nil {
				if restorer, ok := target.(discovery.ServiceKeyRestorer); ok {
					if err := restorer.RestoreServiceKey(ctx, *event.ServiceKey); err != nil {
						return fmt.Errorf("failed to restore service key: %w", err)
					}
				}
			}
		case discovery.EventRevokeServiceKey:
			revoker := discovery.NodeIdentity{Name: "system"}
			if event.RevokedBy != nil {
				revoker = *event.RevokedBy
			}
			_ = target.RevokeServiceKey(ctx, event.RevokeKey, revoker)
		}
	}

	return nil
}

// FindOriginalsStorage locates a storage service tagged with "originals" from discovery.
func FindOriginalsStorage(ctx context.Context, d discovery.Discovery, httpClient *http.Client) (storage.Storage, error) {
	descs, err := d.Find(ctx, "storage-v1", "originals", 1)
	if err != nil {
		return nil, fmt.Errorf("failed to query discovery for originals storage: %w", err)
	}
	if len(descs) == 0 {
		return nil, errors.New("no storage service with tag 'originals' found")
	}
	return storage.NewClient(descs[0].Address, httpClient), nil
}
