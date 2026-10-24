package discovery

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chuckjaz/invariant-go/internal/trace"
)

const (
	maxRedeemFailuresPerWindow = 5
	redeemFailureWindow        = 1 * time.Minute
)

type DiscoveryServer struct {
	id                string
	discovery         Discovery
	controlPlane      ControlPlane
	serviceKeys       ServiceKeyManager
	invitations       InvitationManager
	peerResolver      PeerKeyResolver
	controlPlaneURL   string
	oauthAuth         Authenticator
	staticKeys        map[string]struct{}
	requireAuth       bool
	handler           http.Handler
	tracer            *trace.Tracer
	rateMu            sync.Mutex
	failedRedemptions map[string][]time.Time
}

func NewDiscoveryServer(discovery Discovery) *DiscoveryServer {
	idBytes := make([]byte, 32)
	rand.Read(idBytes)
	id := hex.EncodeToString(idBytes)

	s := &DiscoveryServer{
		id:                id,
		discovery:         discovery,
		staticKeys:        make(map[string]struct{}),
		failedRedemptions: make(map[string][]time.Time),
	}
	if cp, ok := discovery.(ControlPlane); ok {
		s.controlPlane = cp
	}
	if skm, ok := discovery.(ServiceKeyManager); ok {
		s.serviceKeys = skm
	}
	if im, ok := discovery.(InvitationManager); ok {
		s.invitations = im
	}
	s.handler = s.Handler()
	return s
}

// WithControlPlane sets the ControlPlane implementation for the discovery server.
func (s *DiscoveryServer) WithControlPlane(cp ControlPlane) *DiscoveryServer {
	s.controlPlane = cp
	s.handler = s.Handler()
	return s
}

// WithServiceKeyManager sets the ServiceKeyManager for managing service keys.
func (s *DiscoveryServer) WithServiceKeyManager(skm ServiceKeyManager) *DiscoveryServer {
	s.serviceKeys = skm
	s.handler = s.Handler()
	return s
}

// WithInvitationManager sets the InvitationManager for managing invitations and access tokens.
func (s *DiscoveryServer) WithInvitationManager(im InvitationManager) *DiscoveryServer {
	s.invitations = im
	s.handler = s.Handler()
	return s
}

// WithPeerKeyResolver configures the ITP peer key resolver (e.g. *itp.Listener) to enforce
// WireGuard public key pinning on access tokens and invitation redemption.
func (s *DiscoveryServer) WithPeerKeyResolver(r PeerKeyResolver) *DiscoveryServer {
	s.peerResolver = r
	s.handler = s.Handler()
	return s
}

// WithControlPlaneURL configures the control plane / discovery service address (e.g. itp://tc1...)
// embedded into generated invitation codes.
func (s *DiscoveryServer) WithControlPlaneURL(url string) *DiscoveryServer {
	s.controlPlaneURL = strings.TrimSpace(url)
	s.handler = s.Handler()
	return s
}

// WithOAuthAuthenticator sets the Authenticator for validating OAuth user tokens.
func (s *DiscoveryServer) WithOAuthAuthenticator(auth Authenticator) *DiscoveryServer {
	s.oauthAuth = auth
	s.handler = s.Handler()
	return s
}

// WithServiceKeys adds static service keys allowed to authenticate.
func (s *DiscoveryServer) WithServiceKeys(keys ...string) *DiscoveryServer {
	for _, k := range keys {
		if k != "" {
			s.staticKeys[k] = struct{}{}
		}
	}
	s.handler = s.Handler()
	return s
}

// WithRequireAuth explicitly enforces that all requests must have valid credentials.
func (s *DiscoveryServer) WithRequireAuth(require bool) *DiscoveryServer {
	s.requireAuth = require
	s.handler = s.Handler()
	return s
}

// WithTracer attaches a Tracer to the discovery server.
func (s *DiscoveryServer) WithTracer(t *trace.Tracer) *DiscoveryServer {
	s.tracer = t
	s.handler = s.Handler()
	return s
}

func (s *DiscoveryServer) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /id", s.handleGetID)
	mux.HandleFunc("GET /protocols", s.handleGetProtocols)
	mux.HandleFunc("GET /{id}", s.handleGet)
	mux.HandleFunc("GET /", s.handleFind)
	mux.HandleFunc("PUT /{id}", s.handlePut)

	// Control plane (itp-v1) routes
	mux.HandleFunc("POST /auth/client", s.handleRegisterClient)
	mux.HandleFunc("GET /auth/verify", s.handleVerifyKey)
	mux.HandleFunc("GET /auth/keys", s.handleAuthorizedKeys)
	mux.HandleFunc("GET /whois", s.handleWhoIs)

	// Service key routes
	mux.HandleFunc("POST /auth/service-key", s.handleCreateServiceKey)
	mux.HandleFunc("GET /auth/service-keys", s.handleListServiceKeys)
	mux.HandleFunc("GET /auth/verify-service-key", s.handleVerifyServiceKey)
	mux.HandleFunc("DELETE /auth/service-key/{key}", s.handleRevokeServiceKey)

	// Invitation & access token routes
	mux.HandleFunc("POST /auth/invite", s.handleCreateInvitation)
	mux.HandleFunc("POST /auth/invite/redeem", s.handleRedeemInvitation)
	mux.HandleFunc("GET /auth/invites", s.handleListInvitations)
	mux.HandleFunc("DELETE /auth/invite/{id}", s.handleRevokeInvitation)
	mux.HandleFunc("GET /auth/tokens", s.handleListAccessTokens)
	mux.HandleFunc("GET /auth/verify-token", s.handleVerifyAccessToken)
	mux.HandleFunc("DELETE /auth/token/{id}", s.handleRevokeAccessToken)

	h := s.authMiddleware(mux)
	return trace.Middleware("discovery", s.tracer)(h)
}

func (s *DiscoveryServer) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /id is allowed unauthenticated for basic health probing, and
		// POST /auth/invite/redeem is allowed unauthenticated to exchange an invitation code for an access token.
		if r.URL.Path == "/id" || (r.Method == http.MethodPost && r.URL.Path == "/auth/invite/redeem") {
			next.ServeHTTP(w, r)
			return
		}

		mustAuth := s.requireAuth || len(s.staticKeys) > 0 || s.oauthAuth != nil

		authHeader := r.Header.Get("Authorization")
		token := strings.TrimPrefix(authHeader, "Bearer ")
		token = strings.TrimSpace(token)
		if token == "" && authHeader != "" && !strings.HasPrefix(authHeader, "Bearer ") {
			token = strings.TrimSpace(authHeader)
		}

		if token == "" {
			if mustAuth {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			caller := Caller{
				Kind:     CallerKindAnonymous,
				Identity: NodeIdentity{Name: "anonymous"},
			}
			next.ServeHTTP(w, r.WithContext(WithCaller(r.Context(), caller)))
			return
		}

		// 1. Check static service keys
		if _, ok := s.staticKeys[token]; ok {
			caller := Caller{
				Kind:     CallerKindService,
				Identity: NodeIdentity{Name: "service"},
			}
			next.ServeHTTP(w, r.WithContext(WithCaller(r.Context(), caller)))
			return
		}

		// 2. Check InvitationManager Access Tokens
		if s.invitations != nil {
			if ok, tokRec, err := s.invitations.VerifyAccessToken(r.Context(), token); err == nil && ok && tokRec != nil {
				// Enforce WireGuard public key pinning if peerResolver is configured and the token has a pinned key
				if s.peerResolver != nil && tokRec.PublicKey != "" {
					if peerKey, hasPeerKey := s.peerResolver.PeerKey(r.RemoteAddr); hasPeerKey && !peerKey.IsZero() {
						if peerKey.String() != tokRec.PublicKey {
							http.Error(w, "Unauthorized: peer WireGuard key does not match pinned token key", http.StatusUnauthorized)
							return
						}
					}
				}

				kind := CallerKindUser
				switch tokRec.Role {
				case RoleAdmin:
					kind = CallerKindAdmin
				case RoleService:
					kind = CallerKindService
				case RoleUser:
					kind = CallerKindUser
				}

				caller := Caller{
					Kind:        kind,
					Identity:    NodeIdentity{Name: tokRec.Name, Email: tokRec.Email},
					TokenRecord: tokRec,
				}
				next.ServeHTTP(w, r.WithContext(WithCaller(r.Context(), caller)))
				return
			}
		}

		// 3. Check dynamic ServiceKeyManager
		if s.serviceKeys != nil {
			if ok, keyRec, err := s.serviceKeys.VerifyServiceKey(r.Context(), token); err == nil && ok && keyRec != nil {
				caller := Caller{
					Kind:      CallerKindService,
					Identity:  keyRec.CreatedBy,
					KeyRecord: keyRec,
				}
				next.ServeHTTP(w, r.WithContext(WithCaller(r.Context(), caller)))
				return
			}
		}

		// 4. Check OAuth Authenticator
		if s.oauthAuth != nil {
			if ident, err := s.oauthAuth.Authenticate(r.Context(), token); err == nil && ident != nil {
				caller := Caller{
					Kind:     CallerKindUser,
					Identity: *ident,
				}
				next.ServeHTTP(w, r.WithContext(WithCaller(r.Context(), caller)))
				return
			}
		}

		if mustAuth {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		caller := Caller{
			Kind:     CallerKindAnonymous,
			Identity: NodeIdentity{Name: "anonymous"},
		}
		next.ServeHTTP(w, r.WithContext(WithCaller(r.Context(), caller)))
	})
}

func (s *DiscoveryServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *DiscoveryServer) handleGetID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(s.id))
}

func (s *DiscoveryServer) handleGetProtocols(w http.ResponseWriter, r *http.Request) {
	protocols := []string{"discovery-v1"}
	if s.controlPlane != nil {
		protocols = append(protocols, ProtocolControlPlane)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(protocols)
}

func (s *DiscoveryServer) handleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	desc, ok := s.discovery.Get(r.Context(), id)
	if !ok {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(desc)
}

func (s *DiscoveryServer) handleFind(w http.ResponseWriter, r *http.Request) {
	protocol := r.URL.Query().Get("protocol")
	tag := r.URL.Query().Get("tag")
	countStr := r.URL.Query().Get("count")
	count := 1
	if countStr != "" {
		parsedCount, err := strconv.Atoi(countStr)
		if err == nil {
			count = max(parsedCount, 0)
		}
	}

	descs, err := s.discovery.Find(r.Context(), protocol, tag, count)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if descs == nil {
		descs = []ServiceDescription{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(descs)
}

func (s *DiscoveryServer) handlePut(w http.ResponseWriter, r *http.Request) {
	caller, hasCaller := CallerFromContext(r.Context())
	if hasCaller && caller.Kind == CallerKindUser {
		http.Error(w, "Forbidden: user tokens are not permitted to register services", http.StatusForbidden)
		return
	}

	var reg ServiceRegistration
	if err := json.NewDecoder(r.Body).Decode(&reg); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if hasCaller && caller.TokenRecord != nil && caller.TokenRecord.PublicKey != "" {
		if reg.PublicKey != "" && reg.PublicKey != caller.TokenRecord.PublicKey && caller.Kind != CallerKindAdmin {
			http.Error(w, "Forbidden: service public_key does not match pinned token public_key", http.StatusForbidden)
			return
		}
		if reg.PublicKey == "" {
			reg.PublicKey = caller.TokenRecord.PublicKey
		}
	}

	if err := s.discovery.Register(r.Context(), reg); err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

type registerClientRequest struct {
	PublicKey string       `json:"public_key"`
	Identity  NodeIdentity `json:"identity"`
}

type verifyKeyResponse struct {
	Authorized bool          `json:"authorized"`
	Identity   *NodeIdentity `json:"identity,omitempty"`
}

func (s *DiscoveryServer) handleRegisterClient(w http.ResponseWriter, r *http.Request) {
	if s.controlPlane == nil {
		http.Error(w, "Control plane not supported", http.StatusNotImplemented)
		return
	}
	var req registerClientRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	if strings.TrimSpace(req.PublicKey) == "" {
		http.Error(w, "Missing public_key", http.StatusBadRequest)
		return
	}

	caller, hasCaller := CallerFromContext(r.Context())
	if hasCaller && caller.TokenRecord != nil && caller.TokenRecord.PublicKey != "" {
		if req.PublicKey != caller.TokenRecord.PublicKey && caller.Kind != CallerKindAdmin {
			http.Error(w, "Forbidden: access token is pinned to a different WireGuard public key", http.StatusForbidden)
			return
		}
	}
	if hasCaller && (caller.Kind == CallerKindUser || caller.Kind == CallerKindAdmin) {
		if caller.Identity.Name != "" {
			req.Identity.Name = caller.Identity.Name
		}
		if caller.Identity.Email != "" {
			req.Identity.Email = caller.Identity.Email
		}
	}

	if err := s.controlPlane.RegisterClient(r.Context(), req.PublicKey, req.Identity); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *DiscoveryServer) handleVerifyKey(w http.ResponseWriter, r *http.Request) {
	if s.controlPlane == nil {
		http.Error(w, "Control plane not supported", http.StatusNotImplemented)
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "Missing key parameter", http.StatusBadRequest)
		return
	}
	authorized, id, err := s.controlPlane.VerifyKey(r.Context(), key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := verifyKeyResponse{
		Authorized: authorized,
		Identity:   id,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *DiscoveryServer) handleAuthorizedKeys(w http.ResponseWriter, r *http.Request) {
	if s.controlPlane == nil {
		http.Error(w, "Control plane not supported", http.StatusNotImplemented)
		return
	}
	keys, err := s.controlPlane.AuthorizedKeys(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if keys == nil {
		keys = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(keys)
}

func (s *DiscoveryServer) handleWhoIs(w http.ResponseWriter, r *http.Request) {
	if s.controlPlane == nil {
		http.Error(w, "Control plane not supported", http.StatusNotImplemented)
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "Missing key parameter", http.StatusBadRequest)
		return
	}
	authorized, id, err := s.controlPlane.VerifyKey(r.Context(), key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !authorized || id == nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(id)
}

func (s *DiscoveryServer) isAdminCaller(caller Caller) bool {
	if caller.Kind == CallerKindAdmin {
		return true
	}
	// Allow OAuth-authenticated users (TokenRecord == nil) to manage keys/invites when OAuth is used
	if caller.Kind == CallerKindUser && caller.TokenRecord == nil {
		return true
	}
	if !s.requireAuth && len(s.staticKeys) == 0 && s.oauthAuth == nil && caller.Kind == CallerKindAnonymous {
		return true
	}
	return false
}

type createServiceKeyRequest struct {
	Name      string `json:"name"`
	ExpiresIn string `json:"expires_in,omitempty"`
}

func (s *DiscoveryServer) handleCreateServiceKey(w http.ResponseWriter, r *http.Request) {
	if s.serviceKeys == nil {
		http.Error(w, "Service key management not supported", http.StatusNotImplemented)
		return
	}

	caller, _ := CallerFromContext(r.Context())
	if !s.isAdminCaller(caller) {
		http.Error(w, "Only administrators or authenticated users can create service keys", http.StatusForbidden)
		return
	}

	var req createServiceKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if strings.TrimSpace(req.Name) == "" {
		http.Error(w, "Missing key name", http.StatusBadRequest)
		return
	}

	var expiresAt *time.Time
	if req.ExpiresIn != "" {
		dur, err := time.ParseDuration(req.ExpiresIn)
		if err != nil {
			http.Error(w, "Invalid expires_in duration", http.StatusBadRequest)
			return
		}
		t := time.Now().UTC().Add(dur)
		expiresAt = &t
	}

	creator := caller.Identity
	if creator.Name == "" {
		creator.Name = "user"
	}

	rec, err := s.serviceKeys.CreateServiceKey(r.Context(), req.Name, creator, expiresAt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(rec)
}

func (s *DiscoveryServer) handleListServiceKeys(w http.ResponseWriter, r *http.Request) {
	if s.serviceKeys == nil {
		http.Error(w, "Service key management not supported", http.StatusNotImplemented)
		return
	}

	caller, _ := CallerFromContext(r.Context())
	if !s.isAdminCaller(caller) {
		http.Error(w, "Only administrators or authenticated users can list service keys", http.StatusForbidden)
		return
	}

	keys, err := s.serviceKeys.ListServiceKeys(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	masked := make([]ServiceKeyRecord, len(keys))
	for i, k := range keys {
		masked[i] = k.Masked()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(masked)
}

func (s *DiscoveryServer) handleRevokeServiceKey(w http.ResponseWriter, r *http.Request) {
	if s.serviceKeys == nil {
		http.Error(w, "Service key management not supported", http.StatusNotImplemented)
		return
	}

	caller, _ := CallerFromContext(r.Context())
	if !s.isAdminCaller(caller) {
		http.Error(w, "Only administrators or authenticated users can revoke service keys", http.StatusForbidden)
		return
	}

	key := r.PathValue("key")
	if key == "" {
		http.Error(w, "Missing key parameter", http.StatusBadRequest)
		return
	}

	if err := s.serviceKeys.RevokeServiceKey(r.Context(), key, caller.Identity); err != nil {
		if errors.Is(err, ErrServiceKeyNotFound) {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

type verifyServiceKeyResponse struct {
	Valid  bool              `json:"valid"`
	Record *ServiceKeyRecord `json:"record,omitempty"`
}

func (s *DiscoveryServer) handleVerifyServiceKey(w http.ResponseWriter, r *http.Request) {
	if s.serviceKeys == nil {
		http.Error(w, "Service key management not supported", http.StatusNotImplemented)
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "Missing key parameter", http.StatusBadRequest)
		return
	}
	valid, rec, err := s.serviceKeys.VerifyServiceKey(r.Context(), key)
	if err != nil && !errors.Is(err, ErrServiceKeyNotFound) && !errors.Is(err, ErrServiceKeyRevoked) && !errors.Is(err, ErrServiceKeyExpired) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := verifyServiceKeyResponse{
		Valid:  valid,
		Record: rec,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

type createInvitationRequest struct {
	Name string    `json:"name"`
	Role TokenRole `json:"role,omitempty"`
	TTL  string    `json:"ttl,omitempty"`
}

type redeemInvitationRequest struct {
	Code      string       `json:"code"`
	PublicKey string       `json:"public_key,omitempty"`
	Identity  NodeIdentity `json:"identity"`
}

func (s *DiscoveryServer) peerRateLimitKey(r *http.Request) string {
	if s.peerResolver != nil {
		if pk, ok := s.peerResolver.PeerKey(r.RemoteAddr); ok && !pk.IsZero() {
			return pk.String()
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

func (s *DiscoveryServer) isRateLimited(peerKey string) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()

	now := time.Now()
	cutoff := now.Add(-redeemFailureWindow)
	attempts := s.failedRedemptions[peerKey]
	valid := attempts[:0]
	for _, t := range attempts {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	if len(valid) == 0 {
		delete(s.failedRedemptions, peerKey)
		return false
	}
	s.failedRedemptions[peerKey] = valid
	return len(valid) >= maxRedeemFailuresPerWindow
}

func (s *DiscoveryServer) recordFailedRedemption(peerKey string) {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	s.failedRedemptions[peerKey] = append(s.failedRedemptions[peerKey], time.Now())
}

func (s *DiscoveryServer) handleCreateInvitation(w http.ResponseWriter, r *http.Request) {
	if s.invitations == nil {
		http.Error(w, "Invitation management not supported", http.StatusNotImplemented)
		return
	}

	caller, _ := CallerFromContext(r.Context())
	if !s.isAdminCaller(caller) {
		http.Error(w, "Forbidden: only administrators can create invitations", http.StatusForbidden)
		return
	}

	var req createInvitationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if strings.TrimSpace(req.Name) == "" {
		http.Error(w, "Missing invitation name", http.StatusBadRequest)
		return
	}

	role := req.Role
	if role == "" {
		role = RoleUser
	}
	if !ValidRole(role) {
		http.Error(w, ErrInvalidRole.Error(), http.StatusBadRequest)
		return
	}

	var ttl time.Duration
	if req.TTL != "" {
		var err error
		ttl, err = time.ParseDuration(req.TTL)
		if err != nil || ttl <= 0 {
			http.Error(w, "Invalid ttl duration", http.StatusBadRequest)
			return
		}
	}

	creator := caller.Identity
	if creator.Name == "" {
		creator.Name = "admin"
	}

	inv, err := s.invitations.CreateInvitation(r.Context(), req.Name, role, creator, ttl)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.controlPlaneURL != "" {
		if inv.ControlPlaneURL == "" {
			inv.ControlPlaneURL = s.controlPlaneURL
		}
		inv.Code = FormatInvitationCode(inv.Code, inv.ControlPlaneURL)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(inv)
}

func (s *DiscoveryServer) handleRedeemInvitation(w http.ResponseWriter, r *http.Request) {
	if s.invitations == nil {
		http.Error(w, "Invitation management not supported", http.StatusNotImplemented)
		return
	}

	peerRLKey := s.peerRateLimitKey(r)
	if s.isRateLimited(peerRLKey) {
		http.Error(w, "Too Many Requests: rate limit exceeded for invitation redemption", http.StatusTooManyRequests)
		return
	}

	var req redeemInvitationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if strings.TrimSpace(req.Code) == "" {
		http.Error(w, "Missing invitation code", http.StatusBadRequest)
		return
	}

	// Verify proof-of-possession against the ITP tunnel peer key if available
	if s.peerResolver != nil {
		if peerKey, ok := s.peerResolver.PeerKey(r.RemoteAddr); ok && !peerKey.IsZero() {
			if req.PublicKey == "" {
				req.PublicKey = peerKey.String()
			} else if req.PublicKey != peerKey.String() {
				s.recordFailedRedemption(peerRLKey)
				http.Error(w, "Forbidden: submitted public_key does not match ITP tunnel peer key", http.StatusForbidden)
				return
			}
		}
	}

	tokRec, err := s.invitations.RedeemInvitation(r.Context(), req.Code, req.PublicKey, req.Identity)
	if err != nil {
		s.recordFailedRedemption(peerRLKey)
		switch {
		case errors.Is(err, ErrInvitationNotFound):
			http.Error(w, err.Error(), http.StatusNotFound)
		case errors.Is(err, ErrInvitationUsed), errors.Is(err, ErrInvitationExpired), errors.Is(err, ErrInvitationRevoked):
			http.Error(w, err.Error(), http.StatusForbidden)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(tokRec)
}

func (s *DiscoveryServer) handleListInvitations(w http.ResponseWriter, r *http.Request) {
	if s.invitations == nil {
		http.Error(w, "Invitation management not supported", http.StatusNotImplemented)
		return
	}

	caller, _ := CallerFromContext(r.Context())
	if !s.isAdminCaller(caller) {
		http.Error(w, "Forbidden: only administrators can list invitations", http.StatusForbidden)
		return
	}

	invites, err := s.invitations.ListInvitations(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	masked := make([]InvitationRecord, len(invites))
	for i, inv := range invites {
		masked[i] = inv.Masked()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(masked)
}

func (s *DiscoveryServer) handleRevokeInvitation(w http.ResponseWriter, r *http.Request) {
	if s.invitations == nil {
		http.Error(w, "Invitation management not supported", http.StatusNotImplemented)
		return
	}

	caller, _ := CallerFromContext(r.Context())
	if !s.isAdminCaller(caller) {
		http.Error(w, "Forbidden: only administrators can revoke invitations", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing invitation id", http.StatusBadRequest)
		return
	}

	if err := s.invitations.RevokeInvitation(r.Context(), id, caller.Identity); err != nil {
		if errors.Is(err, ErrInvitationNotFound) {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *DiscoveryServer) handleListAccessTokens(w http.ResponseWriter, r *http.Request) {
	if s.invitations == nil {
		http.Error(w, "Access token management not supported", http.StatusNotImplemented)
		return
	}

	caller, _ := CallerFromContext(r.Context())
	if !s.isAdminCaller(caller) {
		http.Error(w, "Forbidden: only administrators can list access tokens", http.StatusForbidden)
		return
	}

	tokens, err := s.invitations.ListAccessTokens(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	masked := make([]AccessTokenRecord, len(tokens))
	for i, tok := range tokens {
		masked[i] = tok.Masked()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(masked)
}

func (s *DiscoveryServer) handleRevokeAccessToken(w http.ResponseWriter, r *http.Request) {
	if s.invitations == nil {
		http.Error(w, "Access token management not supported", http.StatusNotImplemented)
		return
	}

	caller, _ := CallerFromContext(r.Context())
	if !s.isAdminCaller(caller) {
		http.Error(w, "Forbidden: only administrators can revoke access tokens", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing token id", http.StatusBadRequest)
		return
	}

	if err := s.invitations.RevokeAccessToken(r.Context(), id, caller.Identity); err != nil {
		if errors.Is(err, ErrAccessTokenNotFound) {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

type verifyAccessTokenResponse struct {
	Valid  bool               `json:"valid"`
	Record *AccessTokenRecord `json:"record,omitempty"`
}

func (s *DiscoveryServer) handleVerifyAccessToken(w http.ResponseWriter, r *http.Request) {
	if s.invitations == nil {
		http.Error(w, "Access token management not supported", http.StatusNotImplemented)
		return
	}

	targetToken := strings.TrimSpace(r.URL.Query().Get("token"))
	if targetToken == "" {
		http.Error(w, "Missing token query parameter", http.StatusBadRequest)
		return
	}

	valid, rec, err := s.invitations.VerifyAccessToken(r.Context(), targetToken)
	resp := verifyAccessTokenResponse{
		Valid: err == nil && valid,
	}
	if resp.Valid && rec != nil {
		masked := rec.Masked()
		resp.Record = &masked
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
