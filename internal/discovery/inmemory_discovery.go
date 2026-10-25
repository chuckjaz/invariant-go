package discovery

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// Assert that InMemoryDiscovery implements Discovery, ControlPlane, ServiceKeyManager, ServiceKeyRestorer, InvitationManager, and InvitationRestorer interfaces
var (
	_ Discovery          = (*InMemoryDiscovery)(nil)
	_ ControlPlane       = (*InMemoryDiscovery)(nil)
	_ ServiceKeyManager  = (*InMemoryDiscovery)(nil)
	_ ServiceKeyRestorer = (*InMemoryDiscovery)(nil)
	_ InvitationManager  = (*InMemoryDiscovery)(nil)
	_ InvitationRestorer = (*InMemoryDiscovery)(nil)
)

type InMemoryDiscovery struct {
	mu              sync.RWMutex
	services        map[string]ServiceRegistration
	clients         map[string]NodeIdentity
	serviceKeys     map[string]ServiceKeyRecord
	invitations     map[string]InvitationRecord
	accessTokens    map[string]AccessTokenRecord
	controlPlaneURL string
	tracker         *HealthTracker
	sink            EventSink
}

func NewInMemoryDiscovery() *InMemoryDiscovery {
	d := &InMemoryDiscovery{
		services:     make(map[string]ServiceRegistration),
		clients:      make(map[string]NodeIdentity),
		serviceKeys:  make(map[string]ServiceKeyRecord),
		invitations:  make(map[string]InvitationRecord),
		accessTokens: make(map[string]AccessTokenRecord),
	}
	return d
}

func (d *InMemoryDiscovery) WithHealthTracking(interval, timeout time.Duration) *InMemoryDiscovery {
	if d.tracker != nil {
		d.tracker.Close()
	}
	d.tracker = NewHealthTracker(interval, timeout, d.listAll, d.remove)
	return d
}

func (d *InMemoryDiscovery) WithEventSink(sink EventSink) *InMemoryDiscovery {
	d.sink = sink
	return d
}

func (d *InMemoryDiscovery) WithControlPlaneURL(url string) *InMemoryDiscovery {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.controlPlaneURL = strings.TrimSpace(url)
	return d
}

func (d *InMemoryDiscovery) Close() error {
	if d.tracker != nil {
		d.tracker.Close()
	}
	if d.sink != nil {
		_ = d.sink.Close()
	}
	return nil
}

func (d *InMemoryDiscovery) listAll() []ServiceRegistration {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var res []ServiceRegistration
	for _, s := range d.services {
		res = append(res, s)
	}
	return res
}

func (d *InMemoryDiscovery) remove(id string) {
	d.mu.Lock()
	delete(d.services, id)
	sink := d.sink
	d.mu.Unlock()

	if sink != nil {
		_ = sink.Append(context.Background(), DiscoveryEvent{
			Kind:      EventUnregisterService,
			ServiceID: id,
			Timestamp: time.Now().UTC(),
		})
	}
}

func (d *InMemoryDiscovery) Get(ctx context.Context, id string) (ServiceDescription, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	reg, ok := d.services[id]
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

func (d *InMemoryDiscovery) Find(ctx context.Context, protocol, tag string, count int) ([]ServiceDescription, error) {
	d.mu.RLock()
	var results []ServiceDescription
	for _, reg := range d.services {
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
		}
	}
	d.mu.RUnlock()

	// Sort healthy services first
	if d.tracker != nil {
		d.tracker.Sort(results)
	}

	if count > 0 && len(results) > count {
		results = results[:count]
	}

	return results, nil
}

func (d *InMemoryDiscovery) Register(ctx context.Context, reg ServiceRegistration) error {
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
	savedReg := ServiceRegistration{
		ID:        reg.ID,
		Address:   reg.Address,
		Protocols: protocolsCopy,
		Tags:      tagsCopy,
		PublicKey: reg.PublicKey,
		Identity:  idCopy,
	}

	d.mu.Lock()
	d.services[reg.ID] = savedReg
	tracker := d.tracker
	sink := d.sink
	d.mu.Unlock()

	if tracker != nil {
		tracker.MarkHealthy(reg.ID)
	}
	if sink != nil {
		_ = sink.Append(ctx, DiscoveryEvent{
			Kind:      EventRegisterService,
			Service:   &savedReg,
			Timestamp: time.Now().UTC(),
		})
	}
	return nil
}

func (d *InMemoryDiscovery) RegisterClient(ctx context.Context, publicKey string, id NodeIdentity) error {
	d.mu.Lock()
	d.clients[publicKey] = id
	sink := d.sink
	d.mu.Unlock()

	if sink != nil {
		_ = sink.Append(ctx, DiscoveryEvent{
			Kind:        EventRegisterClient,
			ClientKey:   publicKey,
			ClientIdent: &id,
			Timestamp:   time.Now().UTC(),
		})
	}
	return nil
}

func (d *InMemoryDiscovery) VerifyKey(ctx context.Context, publicKey string) (bool, *NodeIdentity, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	hasTokenForKey := false
	for _, tok := range d.accessTokens {
		if tok.PublicKey != "" && tok.PublicKey == publicKey {
			hasTokenForKey = true
			if tok.IsActive() {
				return true, &NodeIdentity{Name: tok.Name, Email: tok.Email}, nil
			}
		}
	}
	if hasTokenForKey {
		return false, nil, nil
	}

	if id, ok := d.clients[publicKey]; ok {
		idCopy := id
		return true, &idCopy, nil
	}

	for _, reg := range d.services {
		if reg.PublicKey != "" && reg.PublicKey == publicKey {
			if reg.Identity != nil {
				idCopy := *reg.Identity
				return true, &idCopy, nil
			}
			return true, &NodeIdentity{Name: reg.ID}, nil
		}
	}

	return false, nil, nil
}

func (d *InMemoryDiscovery) AuthorizedKeys(ctx context.Context) ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	activeTokenKeys := make(map[string]bool)
	revokedTokenKeys := make(map[string]bool)
	for _, tok := range d.accessTokens {
		if tok.PublicKey == "" {
			continue
		}
		if tok.IsActive() {
			activeTokenKeys[tok.PublicKey] = true
		} else {
			revokedTokenKeys[tok.PublicKey] = true
		}
	}

	keySet := make(map[string]struct{})
	for k := range activeTokenKeys {
		keySet[k] = struct{}{}
	}
	for k := range d.clients {
		if revokedTokenKeys[k] && !activeTokenKeys[k] {
			continue
		}
		keySet[k] = struct{}{}
	}
	for _, reg := range d.services {
		if reg.PublicKey != "" {
			if revokedTokenKeys[reg.PublicKey] && !activeTokenKeys[reg.PublicKey] {
				continue
			}
			keySet[reg.PublicKey] = struct{}{}
		}
	}

	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys, nil
}

func (d *InMemoryDiscovery) CreateServiceKey(ctx context.Context, name string, creator NodeIdentity, expiresAt *time.Time) (*ServiceKeyRecord, error) {
	rec, err := GenerateServiceKey(name, creator, expiresAt)
	if err != nil {
		return nil, err
	}

	d.mu.Lock()
	d.serviceKeys[rec.Key] = *rec
	sink := d.sink
	d.mu.Unlock()

	if sink != nil {
		_ = sink.Append(ctx, DiscoveryEvent{
			Kind:       EventCreateServiceKey,
			ServiceKey: rec,
			Timestamp:  time.Now().UTC(),
		})
	}

	recCopy := *rec
	return &recCopy, nil
}

func (d *InMemoryDiscovery) VerifyServiceKey(ctx context.Context, key string) (bool, *ServiceKeyRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rec, ok := d.serviceKeys[key]
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

func (d *InMemoryDiscovery) ListServiceKeys(ctx context.Context) ([]ServiceKeyRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	records := make([]ServiceKeyRecord, 0, len(d.serviceKeys))
	for _, rec := range d.serviceKeys {
		records = append(records, rec)
	}
	slices.SortFunc(records, func(a, b ServiceKeyRecord) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return records, nil
}

func (d *InMemoryDiscovery) RevokeServiceKey(ctx context.Context, keyOrID string, revoker NodeIdentity) error {
	d.mu.Lock()
	var foundKey string
	var rec ServiceKeyRecord
	for k, r := range d.serviceKeys {
		if k == keyOrID || r.ID == keyOrID {
			foundKey = k
			rec = r
			break
		}
	}
	if foundKey == "" {
		d.mu.Unlock()
		return ErrServiceKeyNotFound
	}

	now := time.Now().UTC()
	rec.Revoked = true
	rec.RevokedAt = &now
	rec.RevokedBy = &revoker
	d.serviceKeys[foundKey] = rec
	sink := d.sink
	d.mu.Unlock()

	if sink != nil {
		_ = sink.Append(ctx, DiscoveryEvent{
			Kind:      EventRevokeServiceKey,
			RevokeKey: foundKey,
			RevokedBy: &revoker,
			Timestamp: now,
		})
	}
	return nil
}

// RestoreServiceKey directly restores an existing service key record during replay or disaster recovery.
func (d *InMemoryDiscovery) RestoreServiceKey(ctx context.Context, rec ServiceKeyRecord) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.serviceKeys[rec.Key] = rec
	return nil
}

func (d *InMemoryDiscovery) CreateInvitation(ctx context.Context, name string, role TokenRole, creator NodeIdentity, ttl time.Duration, email ...string) (*InvitationRecord, error) {
	inv, err := GenerateInvitation(name, role, creator, ttl, email...)
	if err != nil {
		return nil, err
	}

	d.mu.Lock()
	if d.controlPlaneURL != "" {
		inv.ControlPlaneURL = d.controlPlaneURL
		inv.Code = FormatInvitationCode(inv.Code, d.controlPlaneURL)
	}
	rawCode, _ := ParseInvitationCode(inv.Code)
	d.invitations[rawCode] = *inv
	sink := d.sink
	d.mu.Unlock()

	if sink != nil {
		_ = sink.Append(ctx, DiscoveryEvent{
			Kind:       EventCreateInvitation,
			Invitation: inv,
			Timestamp:  time.Now().UTC(),
		})
	}

	invCopy := *inv
	return &invCopy, nil
}

func (d *InMemoryDiscovery) RedeemInvitation(ctx context.Context, code string, publicKey string, identity NodeIdentity) (*AccessTokenRecord, error) {
	rawCode, cpURL := ParseInvitationCode(code)
	publicKey = strings.TrimSpace(publicKey)

	d.mu.Lock()
	inv, ok := d.invitations[rawCode]
	if !ok {
		inv, ok = d.invitations[strings.TrimSpace(code)]
	}
	if !ok {
		d.mu.Unlock()
		return nil, ErrInvitationNotFound
	}
	if inv.Revoked {
		d.mu.Unlock()
		return nil, ErrInvitationRevoked
	}
	if inv.Used {
		d.mu.Unlock()
		return nil, ErrInvitationUsed
	}
	if time.Now().After(inv.ExpiresAt) {
		d.mu.Unlock()
		return nil, ErrInvitationExpired
	}

	identEmail := strings.TrimSpace(identity.Email)
	invEmail := strings.TrimSpace(inv.Email)
	if inv.Role == RoleUser && identEmail == "" {
		d.mu.Unlock()
		return nil, ErrInvitationOAuthRequired
	}
	if invEmail != "" {
		if identEmail == "" {
			d.mu.Unlock()
			return nil, ErrInvitationOAuthRequired
		}
		if !strings.EqualFold(invEmail, identEmail) {
			d.mu.Unlock()
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
		d.mu.Unlock()
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
	d.invitations[rawCode] = inv
	d.accessTokens[tokRec.Token] = *tokRec
	if publicKey != "" {
		d.clients[publicKey] = NodeIdentity{Name: name, Email: email}
	}
	sink := d.sink
	d.mu.Unlock()

	if sink != nil {
		invCopy := inv
		_ = sink.Append(ctx, DiscoveryEvent{
			Kind:        EventRedeemInvitation,
			Invitation:  &invCopy,
			AccessToken: tokRec,
			Timestamp:   now,
		})
	}

	ret := *tokRec
	return &ret, nil
}

func (d *InMemoryDiscovery) ListInvitations(ctx context.Context) ([]InvitationRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	records := make([]InvitationRecord, 0, len(d.invitations))
	for _, r := range d.invitations {
		records = append(records, r)
	}
	slices.SortFunc(records, func(a, b InvitationRecord) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return records, nil
}

func (d *InMemoryDiscovery) RevokeInvitation(ctx context.Context, codeOrID string, revoker NodeIdentity) error {
	rawTarget, _ := ParseInvitationCode(codeOrID)
	d.mu.Lock()
	var foundCode string
	var inv InvitationRecord
	for c, r := range d.invitations {
		if c == codeOrID || c == rawTarget || r.Code == codeOrID || r.ID == codeOrID {
			foundCode = c
			inv = r
			break
		}
	}
	if foundCode == "" {
		d.mu.Unlock()
		return ErrInvitationNotFound
	}

	now := time.Now().UTC()
	inv.Revoked = true
	inv.RevokedAt = &now
	inv.RevokedBy = &revoker
	d.invitations[foundCode] = inv
	sink := d.sink
	d.mu.Unlock()

	if sink != nil {
		_ = sink.Append(ctx, DiscoveryEvent{
			Kind:      EventRevokeInvitation,
			RevokeKey: foundCode,
			RevokedBy: &revoker,
			Timestamp: now,
		})
	}
	return nil
}

func (d *InMemoryDiscovery) VerifyAccessToken(ctx context.Context, token string) (bool, *AccessTokenRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rec, ok := d.accessTokens[token]
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

func (d *InMemoryDiscovery) ListAccessTokens(ctx context.Context) ([]AccessTokenRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	records := make([]AccessTokenRecord, 0, len(d.accessTokens))
	for _, r := range d.accessTokens {
		records = append(records, r)
	}
	slices.SortFunc(records, func(a, b AccessTokenRecord) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return records, nil
}

func (d *InMemoryDiscovery) RevokeAccessToken(ctx context.Context, tokenOrID string, revoker NodeIdentity) error {
	d.mu.Lock()
	var foundToken string
	var rec AccessTokenRecord
	for t, r := range d.accessTokens {
		if t == tokenOrID || r.ID == tokenOrID {
			foundToken = t
			rec = r
			break
		}
	}
	if foundToken == "" {
		d.mu.Unlock()
		return ErrAccessTokenNotFound
	}

	now := time.Now().UTC()
	rec.Revoked = true
	rec.RevokedAt = &now
	rec.RevokedBy = &revoker
	d.accessTokens[foundToken] = rec

	if rec.PublicKey != "" {
		stillActive := false
		for _, other := range d.accessTokens {
			if other.PublicKey == rec.PublicKey && other.IsActive() {
				stillActive = true
				break
			}
		}
		if !stillActive {
			delete(d.clients, rec.PublicKey)
			for svcID, svc := range d.services {
				if svc.PublicKey == rec.PublicKey {
					delete(d.services, svcID)
				}
			}
		}
	}

	sink := d.sink
	d.mu.Unlock()

	if sink != nil {
		_ = sink.Append(ctx, DiscoveryEvent{
			Kind:      EventRevokeAccessToken,
			RevokeKey: foundToken,
			RevokedBy: &revoker,
			Timestamp: now,
		})
	}
	return nil
}

func (d *InMemoryDiscovery) RestoreInvitation(ctx context.Context, rec InvitationRecord) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	rawCode, _ := ParseInvitationCode(rec.Code)
	d.invitations[rawCode] = rec
	return nil
}

func (d *InMemoryDiscovery) RestoreAccessToken(ctx context.Context, rec AccessTokenRecord) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.accessTokens[rec.Token] = rec
	if rec.PublicKey != "" && rec.IsActive() {
		d.clients[rec.PublicKey] = NodeIdentity{Name: rec.Name, Email: rec.Email}
	}
	return nil
}
