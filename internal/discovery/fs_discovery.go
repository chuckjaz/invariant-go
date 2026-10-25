package discovery

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chuckjaz/invariant-go/internal/journal"
)

// Assert that FileSystemDiscovery implements Discovery, ControlPlane, ServiceKeyManager, ServiceKeyRestorer, InvitationManager, and InvitationRestorer interfaces
var (
	_ Discovery          = (*FileSystemDiscovery)(nil)
	_ ControlPlane       = (*FileSystemDiscovery)(nil)
	_ ServiceKeyManager  = (*FileSystemDiscovery)(nil)
	_ ServiceKeyRestorer = (*FileSystemDiscovery)(nil)
	_ InvitationManager  = (*FileSystemDiscovery)(nil)
	_ InvitationRestorer = (*FileSystemDiscovery)(nil)
)

type FileSystemDiscovery struct {
	store           *journal.Store[string, ServiceRegistration]
	clientStore     *journal.Store[string, NodeIdentity]
	keyStore        *journal.Store[string, ServiceKeyRecord]
	inviteStore     *journal.Store[string, InvitationRecord]
	tokenStore      *journal.Store[string, AccessTokenRecord]
	controlPlaneURL string
	redeemMu        sync.Mutex
	tracker         *HealthTracker
	sink            EventSink
	closeOnce       sync.Once
	closeErr        error
}

func NewFileSystemDiscovery(baseDir string, snapshotInterval time.Duration) (*FileSystemDiscovery, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, err
	}

	store, err := journal.NewStore[string, ServiceRegistration](baseDir, snapshotInterval)
	if err != nil {
		return nil, err
	}

	clientsDir := filepath.Join(baseDir, "clients")
	if err := os.MkdirAll(clientsDir, 0755); err != nil {
		store.Close()
		return nil, err
	}

	clientStore, err := journal.NewStore[string, NodeIdentity](clientsDir, snapshotInterval)
	if err != nil {
		store.Close()
		return nil, err
	}

	keysDir := filepath.Join(baseDir, "service_keys")
	if err := os.MkdirAll(keysDir, 0700); err != nil {
		store.Close()
		clientStore.Close()
		return nil, err
	}

	keyStore, err := journal.NewStore[string, ServiceKeyRecord](keysDir, snapshotInterval)
	if err != nil {
		store.Close()
		clientStore.Close()
		return nil, err
	}

	invitesDir := filepath.Join(baseDir, "invitations")
	if err := os.MkdirAll(invitesDir, 0700); err != nil {
		store.Close()
		clientStore.Close()
		keyStore.Close()
		return nil, err
	}

	inviteStore, err := journal.NewStore[string, InvitationRecord](invitesDir, snapshotInterval)
	if err != nil {
		store.Close()
		clientStore.Close()
		keyStore.Close()
		return nil, err
	}

	tokensDir := filepath.Join(baseDir, "access_tokens")
	if err := os.MkdirAll(tokensDir, 0700); err != nil {
		store.Close()
		clientStore.Close()
		keyStore.Close()
		inviteStore.Close()
		return nil, err
	}

	tokenStore, err := journal.NewStore[string, AccessTokenRecord](tokensDir, snapshotInterval)
	if err != nil {
		store.Close()
		clientStore.Close()
		keyStore.Close()
		inviteStore.Close()
		return nil, err
	}

	d := &FileSystemDiscovery{
		store:       store,
		clientStore: clientStore,
		keyStore:    keyStore,
		inviteStore: inviteStore,
		tokenStore:  tokenStore,
	}

	return d, nil
}

func (d *FileSystemDiscovery) WithHealthTracking(interval, timeout time.Duration) *FileSystemDiscovery {
	if d.tracker != nil {
		d.tracker.Close()
	}

	listFn := func() []ServiceRegistration {
		var res []ServiceRegistration
		d.store.Read(func(m map[string]ServiceRegistration) {
			for _, r := range m {
				res = append(res, r)
			}
		})
		return res
	}

	removeFn := func(id string) {
		d.store.Delete(id, nil)
		if d.sink != nil {
			_ = d.sink.Append(context.Background(), DiscoveryEvent{
				Kind:      EventUnregisterService,
				ServiceID: id,
				Timestamp: time.Now().UTC(),
			})
		}
	}

	d.tracker = NewHealthTracker(interval, timeout, listFn, removeFn)
	return d
}

func (d *FileSystemDiscovery) WithEventSink(sink EventSink) *FileSystemDiscovery {
	d.sink = sink
	return d
}

func (d *FileSystemDiscovery) WithControlPlaneURL(url string) *FileSystemDiscovery {
	d.redeemMu.Lock()
	defer d.redeemMu.Unlock()
	d.controlPlaneURL = strings.TrimSpace(url)
	return d
}

func (d *FileSystemDiscovery) Close() error {
	d.closeOnce.Do(func() {
		if d.tracker != nil {
			d.tracker.Close()
		}
		if d.clientStore != nil {
			d.clientStore.Close()
		}
		if d.keyStore != nil {
			d.keyStore.Close()
		}
		if d.inviteStore != nil {
			d.inviteStore.Close()
		}
		if d.tokenStore != nil {
			d.tokenStore.Close()
		}
		if d.sink != nil {
			_ = d.sink.Close()
		}
		d.closeErr = d.store.Close()
	})
	return d.closeErr
}

func (d *FileSystemDiscovery) Snapshot() error {
	if d.clientStore != nil {
		if err := d.clientStore.Snapshot(); err != nil {
			return err
		}
	}
	if d.keyStore != nil {
		if err := d.keyStore.Snapshot(); err != nil {
			return err
		}
	}
	if d.inviteStore != nil {
		if err := d.inviteStore.Snapshot(); err != nil {
			return err
		}
	}
	if d.tokenStore != nil {
		if err := d.tokenStore.Snapshot(); err != nil {
			return err
		}
	}
	return d.store.Snapshot()
}

func (d *FileSystemDiscovery) Get(ctx context.Context, id string) (ServiceDescription, bool) {
	reg, ok := d.store.Get(id)
	if !ok {
		return ServiceDescription{}, false
	}

	protocolsCopy := make([]string, len(reg.Protocols))
	copy(protocolsCopy, reg.Protocols)

	var tagsCopy []string
	if reg.Tags != nil {
		tagsCopy = make([]string, len(reg.Tags))
		copy(tagsCopy, reg.Tags)
	}

	var idCopy *NodeIdentity
	if reg.Identity != nil {
		idCopy = &NodeIdentity{Name: reg.Identity.Name, Email: reg.Identity.Email}
	}

	return ServiceDescription{
		ID:        reg.ID,
		Address:   reg.Address,
		Protocols: protocolsCopy,
		Tags:      tagsCopy,
		PublicKey: reg.PublicKey,
		Identity:  idCopy,
	}, true
}

func (d *FileSystemDiscovery) Find(ctx context.Context, protocol, tag string, count int) ([]ServiceDescription, error) {
	var results []ServiceDescription
	d.store.Read(func(store map[string]ServiceRegistration) {
		for _, reg := range store {
			hasProtocol := protocol == "" || slices.Contains(reg.Protocols, protocol)
			hasTag := tag == "" || slices.Contains(reg.Tags, tag)
			if hasProtocol && hasTag {
				protocolsCopy := make([]string, len(reg.Protocols))
				copy(protocolsCopy, reg.Protocols)
				var tagsCopy []string
				if reg.Tags != nil {
					tagsCopy = make([]string, len(reg.Tags))
					copy(tagsCopy, reg.Tags)
				}
				var idCopy *NodeIdentity
				if reg.Identity != nil {
					idCopy = &NodeIdentity{Name: reg.Identity.Name, Email: reg.Identity.Email}
				}
				results = append(results, ServiceDescription{
					ID:        reg.ID,
					Address:   reg.Address,
					Protocols: protocolsCopy,
					Tags:      tagsCopy,
					PublicKey: reg.PublicKey,
					Identity:  idCopy,
				})
				if count > 0 && len(results) >= count {
					break
				}
			}
		}
	})

	if d.tracker != nil {
		d.tracker.Sort(results)
	}
	if count > 0 && len(results) > count {
		results = results[:count]
	}

	return results, nil
}

func (d *FileSystemDiscovery) Register(ctx context.Context, reg ServiceRegistration) error {
	protocolsCopy := make([]string, len(reg.Protocols))
	copy(protocolsCopy, reg.Protocols)
	var tagsCopy []string
	if reg.Tags != nil {
		tagsCopy = make([]string, len(reg.Tags))
		copy(tagsCopy, reg.Tags)
	}
	var idCopy *NodeIdentity
	if reg.Identity != nil {
		idCopy = &NodeIdentity{Name: reg.Identity.Name, Email: reg.Identity.Email}
	}
	regCopy := ServiceRegistration{
		ID:        reg.ID,
		Address:   reg.Address,
		Protocols: protocolsCopy,
		Tags:      tagsCopy,
		PublicKey: reg.PublicKey,
		Identity:  idCopy,
	}

	err := d.store.Put(reg.ID, regCopy, nil)
	if err == nil {
		if d.tracker != nil {
			d.tracker.MarkHealthy(reg.ID)
		}
		if d.sink != nil {
			_ = d.sink.Append(ctx, DiscoveryEvent{
				Kind:      EventRegisterService,
				Service:   &regCopy,
				Timestamp: time.Now().UTC(),
			})
		}
	}
	return err
}

func (d *FileSystemDiscovery) RegisterClient(ctx context.Context, publicKey string, id NodeIdentity) error {
	err := d.clientStore.Put(publicKey, id, nil)
	if err == nil && d.sink != nil {
		_ = d.sink.Append(ctx, DiscoveryEvent{
			Kind:        EventRegisterClient,
			ClientKey:   publicKey,
			ClientIdent: &id,
			Timestamp:   time.Now().UTC(),
		})
	}
	return err
}

func (d *FileSystemDiscovery) VerifyKey(ctx context.Context, publicKey string) (bool, *NodeIdentity, error) {
	var hasTokenForKey bool
	var activeTokenIdent *NodeIdentity
	d.tokenStore.Read(func(m map[string]AccessTokenRecord) {
		for _, tok := range m {
			if tok.PublicKey != "" && tok.PublicKey == publicKey {
				hasTokenForKey = true
				if tok.IsActive() {
					activeTokenIdent = &NodeIdentity{Name: tok.Name, Email: tok.Email}
					break
				}
			}
		}
	})
	if activeTokenIdent != nil {
		return true, activeTokenIdent, nil
	}
	if hasTokenForKey {
		return false, nil, nil
	}

	if id, ok := d.clientStore.Get(publicKey); ok {
		idCopy := id
		return true, &idCopy, nil
	}

	var found bool
	var identity *NodeIdentity
	d.store.Read(func(m map[string]ServiceRegistration) {
		for _, reg := range m {
			if reg.PublicKey != "" && reg.PublicKey == publicKey {
				found = true
				if reg.Identity != nil {
					idCopy := *reg.Identity
					identity = &idCopy
				} else {
					identity = &NodeIdentity{Name: reg.ID}
				}
				break
			}
		}
	})

	return found, identity, nil
}

func (d *FileSystemDiscovery) AuthorizedKeys(ctx context.Context) ([]string, error) {
	activeTokenKeys := make(map[string]bool)
	revokedTokenKeys := make(map[string]bool)
	d.tokenStore.Read(func(m map[string]AccessTokenRecord) {
		for _, tok := range m {
			if tok.PublicKey == "" {
				continue
			}
			if tok.IsActive() {
				activeTokenKeys[tok.PublicKey] = true
			} else {
				revokedTokenKeys[tok.PublicKey] = true
			}
		}
	})

	keySet := make(map[string]struct{})
	for k := range activeTokenKeys {
		keySet[k] = struct{}{}
	}
	d.clientStore.Read(func(m map[string]NodeIdentity) {
		for k := range m {
			if revokedTokenKeys[k] && !activeTokenKeys[k] {
				continue
			}
			keySet[k] = struct{}{}
		}
	})
	d.store.Read(func(m map[string]ServiceRegistration) {
		for _, reg := range m {
			if reg.PublicKey != "" {
				if revokedTokenKeys[reg.PublicKey] && !activeTokenKeys[reg.PublicKey] {
					continue
				}
				keySet[reg.PublicKey] = struct{}{}
			}
		}
	})

	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys, nil
}

func (d *FileSystemDiscovery) CreateServiceKey(ctx context.Context, name string, creator NodeIdentity, expiresAt *time.Time) (*ServiceKeyRecord, error) {
	rec, err := GenerateServiceKey(name, creator, expiresAt)
	if err != nil {
		return nil, err
	}

	if err := d.keyStore.Put(rec.Key, *rec, nil); err != nil {
		return nil, err
	}

	if d.sink != nil {
		_ = d.sink.Append(ctx, DiscoveryEvent{
			Kind:       EventCreateServiceKey,
			ServiceKey: rec,
			Timestamp:  time.Now().UTC(),
		})
	}

	recCopy := *rec
	return &recCopy, nil
}

func (d *FileSystemDiscovery) VerifyServiceKey(ctx context.Context, key string) (bool, *ServiceKeyRecord, error) {
	rec, ok := d.keyStore.Get(key)
	if !ok {
		return false, nil, ErrServiceKeyNotFound
	}
	if rec.Revoked {
		return false, nil, ErrServiceKeyRevoked
	}
	if rec.ExpiresAt != nil && time.Now().After(*rec.ExpiresAt) {
		return false, nil, ErrServiceKeyExpired
	}
	recCopy := rec
	return true, &recCopy, nil
}

func (d *FileSystemDiscovery) ListServiceKeys(ctx context.Context) ([]ServiceKeyRecord, error) {
	var records []ServiceKeyRecord
	d.keyStore.Read(func(m map[string]ServiceKeyRecord) {
		for _, r := range m {
			records = append(records, r)
		}
	})
	slices.SortFunc(records, func(a, b ServiceKeyRecord) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return records, nil
}

func (d *FileSystemDiscovery) RevokeServiceKey(ctx context.Context, keyOrID string, revoker NodeIdentity) error {
	var foundKey string
	var rec ServiceKeyRecord
	d.keyStore.Read(func(m map[string]ServiceKeyRecord) {
		for k, r := range m {
			if k == keyOrID || r.ID == keyOrID {
				foundKey = k
				rec = r
				break
			}
		}
	})
	if foundKey == "" {
		return ErrServiceKeyNotFound
	}

	now := time.Now().UTC()
	rec.Revoked = true
	rec.RevokedAt = &now
	rec.RevokedBy = &revoker
	if err := d.keyStore.Put(foundKey, rec, nil); err != nil {
		return err
	}

	if d.sink != nil {
		_ = d.sink.Append(ctx, DiscoveryEvent{
			Kind:      EventRevokeServiceKey,
			RevokeKey: foundKey,
			RevokedBy: &revoker,
			Timestamp: now,
		})
	}
	return nil
}

// RestoreServiceKey directly restores an existing service key record during replay or disaster recovery.
func (d *FileSystemDiscovery) RestoreServiceKey(ctx context.Context, rec ServiceKeyRecord) error {
	return d.keyStore.Put(rec.Key, rec, nil)
}

func (d *FileSystemDiscovery) CreateInvitation(ctx context.Context, name string, role TokenRole, creator NodeIdentity, ttl time.Duration, email ...string) (*InvitationRecord, error) {
	inv, err := GenerateInvitation(name, role, creator, ttl, email...)
	if err != nil {
		return nil, err
	}

	d.redeemMu.Lock()
	cpURL := d.controlPlaneURL
	d.redeemMu.Unlock()
	if cpURL != "" {
		inv.ControlPlaneURL = cpURL
		inv.Code = FormatInvitationCode(inv.Code, cpURL)
	}
	rawCode, _ := ParseInvitationCode(inv.Code)

	if err := d.inviteStore.Put(rawCode, *inv, nil); err != nil {
		return nil, err
	}

	if d.sink != nil {
		_ = d.sink.Append(ctx, DiscoveryEvent{
			Kind:       EventCreateInvitation,
			Invitation: inv,
			Timestamp:  time.Now().UTC(),
		})
	}

	invCopy := *inv
	return &invCopy, nil
}

func (d *FileSystemDiscovery) RedeemInvitation(ctx context.Context, code string, publicKey string, identity NodeIdentity) (*AccessTokenRecord, error) {
	rawCode, cpURL := ParseInvitationCode(code)
	publicKey = strings.TrimSpace(publicKey)

	d.redeemMu.Lock()
	defer d.redeemMu.Unlock()

	storeKey := rawCode
	inv, ok := d.inviteStore.Get(rawCode)
	if !ok {
		storeKey = strings.TrimSpace(code)
		inv, ok = d.inviteStore.Get(storeKey)
	}
	if !ok {
		return nil, ErrInvitationNotFound
	}
	if inv.Revoked {
		return nil, ErrInvitationRevoked
	}
	if inv.Used {
		return nil, ErrInvitationUsed
	}
	if time.Now().After(inv.ExpiresAt) {
		return nil, ErrInvitationExpired
	}

	identEmail := strings.TrimSpace(identity.Email)
	invEmail := strings.TrimSpace(inv.Email)
	if inv.Role == RoleUser && identEmail == "" {
		return nil, ErrInvitationOAuthRequired
	}
	if invEmail != "" {
		if identEmail == "" {
			return nil, ErrInvitationOAuthRequired
		}
		if !strings.EqualFold(invEmail, identEmail) {
			return nil, ErrInvitationEmailMismatch
		}
	}

	name := inv.Name
	if name == "" {
		name = identity.Name
	}
	if name == "" {
		name = string(inv.Role)
	}
	email := inv.Email
	if email == "" {
		email = identity.Email
	}

	tokRec, err := GenerateAccessToken(name, email, inv.Role, publicKey, inv.ID, inv.CreatedBy, nil)
	if err != nil {
		return nil, err
	}
	if inv.ControlPlaneURL != "" {
		tokRec.ControlPlaneURL = inv.ControlPlaneURL
	} else if cpURL != "" {
		tokRec.ControlPlaneURL = cpURL
	} else if d.controlPlaneURL != "" {
		tokRec.ControlPlaneURL = d.controlPlaneURL
	}

	now := time.Now().UTC()
	inv.Used = true
	inv.UsedAt = &now
	inv.UsedByKey = publicKey
	if err := d.inviteStore.Put(storeKey, inv, nil); err != nil {
		return nil, err
	}
	if err := d.tokenStore.Put(tokRec.Token, *tokRec, nil); err != nil {
		return nil, err
	}
	if publicKey != "" {
		_ = d.clientStore.Put(publicKey, NodeIdentity{Name: name, Email: email}, nil)
	}

	if d.sink != nil {
		invCopy := inv
		_ = d.sink.Append(ctx, DiscoveryEvent{
			Kind:        EventRedeemInvitation,
			Invitation:  &invCopy,
			AccessToken: tokRec,
			Timestamp:   now,
		})
	}

	ret := *tokRec
	return &ret, nil
}

func (d *FileSystemDiscovery) ListInvitations(ctx context.Context) ([]InvitationRecord, error) {
	var records []InvitationRecord
	d.inviteStore.Read(func(m map[string]InvitationRecord) {
		for _, r := range m {
			records = append(records, r)
		}
	})
	slices.SortFunc(records, func(a, b InvitationRecord) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return records, nil
}

func (d *FileSystemDiscovery) RevokeInvitation(ctx context.Context, codeOrID string, revoker NodeIdentity) error {
	rawTarget, _ := ParseInvitationCode(codeOrID)
	d.redeemMu.Lock()
	defer d.redeemMu.Unlock()

	var foundCode string
	var inv InvitationRecord
	d.inviteStore.Read(func(m map[string]InvitationRecord) {
		for c, r := range m {
			if c == codeOrID || c == rawTarget || r.Code == codeOrID || r.ID == codeOrID {
				foundCode = c
				inv = r
				break
			}
		}
	})
	if foundCode == "" {
		return ErrInvitationNotFound
	}

	now := time.Now().UTC()
	inv.Revoked = true
	inv.RevokedAt = &now
	inv.RevokedBy = &revoker
	if err := d.inviteStore.Put(foundCode, inv, nil); err != nil {
		return err
	}

	if d.sink != nil {
		_ = d.sink.Append(ctx, DiscoveryEvent{
			Kind:      EventRevokeInvitation,
			RevokeKey: foundCode,
			RevokedBy: &revoker,
			Timestamp: now,
		})
	}
	return nil
}

func (d *FileSystemDiscovery) VerifyAccessToken(ctx context.Context, token string) (bool, *AccessTokenRecord, error) {
	rec, ok := d.tokenStore.Get(token)
	if !ok {
		return false, nil, ErrAccessTokenNotFound
	}
	if rec.Revoked {
		return false, nil, ErrAccessTokenRevoked
	}
	if rec.ExpiresAt != nil && time.Now().After(*rec.ExpiresAt) {
		return false, nil, ErrAccessTokenExpired
	}
	recCopy := rec
	return true, &recCopy, nil
}

func (d *FileSystemDiscovery) ListAccessTokens(ctx context.Context) ([]AccessTokenRecord, error) {
	var records []AccessTokenRecord
	d.tokenStore.Read(func(m map[string]AccessTokenRecord) {
		for _, r := range m {
			records = append(records, r)
		}
	})
	slices.SortFunc(records, func(a, b AccessTokenRecord) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return records, nil
}

func (d *FileSystemDiscovery) RevokeAccessToken(ctx context.Context, tokenOrID string, revoker NodeIdentity) error {
	d.redeemMu.Lock()
	defer d.redeemMu.Unlock()

	var foundToken string
	var rec AccessTokenRecord
	d.tokenStore.Read(func(m map[string]AccessTokenRecord) {
		for t, r := range m {
			if t == tokenOrID || r.ID == tokenOrID {
				foundToken = t
				rec = r
				break
			}
		}
	})
	if foundToken == "" {
		return ErrAccessTokenNotFound
	}

	now := time.Now().UTC()
	rec.Revoked = true
	rec.RevokedAt = &now
	rec.RevokedBy = &revoker
	if err := d.tokenStore.Put(foundToken, rec, nil); err != nil {
		return err
	}

	if rec.PublicKey != "" {
		var stillActive bool
		d.tokenStore.Read(func(m map[string]AccessTokenRecord) {
			for _, other := range m {
				if other.PublicKey == rec.PublicKey && other.IsActive() {
					stillActive = true
					break
				}
			}
		})
		if !stillActive {
			_ = d.clientStore.Delete(rec.PublicKey, nil)
			var svcIDsToRemove []string
			d.store.Read(func(m map[string]ServiceRegistration) {
				for svcID, svc := range m {
					if svc.PublicKey == rec.PublicKey {
						svcIDsToRemove = append(svcIDsToRemove, svcID)
					}
				}
			})
			for _, svcID := range svcIDsToRemove {
				_ = d.store.Delete(svcID, nil)
			}
		}
	}

	if d.sink != nil {
		_ = d.sink.Append(ctx, DiscoveryEvent{
			Kind:      EventRevokeAccessToken,
			RevokeKey: foundToken,
			RevokedBy: &revoker,
			Timestamp: now,
		})
	}
	return nil
}

func (d *FileSystemDiscovery) RestoreInvitation(ctx context.Context, rec InvitationRecord) error {
	rawCode, _ := ParseInvitationCode(rec.Code)
	return d.inviteStore.Put(rawCode, rec, nil)
}

func (d *FileSystemDiscovery) RestoreAccessToken(ctx context.Context, rec AccessTokenRecord) error {
	if err := d.tokenStore.Put(rec.Token, rec, nil); err != nil {
		return err
	}
	if rec.PublicKey != "" && rec.IsActive() {
		_ = d.clientStore.Put(rec.PublicKey, NodeIdentity{Name: rec.Name, Email: rec.Email}, nil)
	}
	return nil
}
