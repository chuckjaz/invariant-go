package discovery

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"invariant/internal/trace"
)

type DiscoveryServer struct {
	id           string
	discovery    Discovery
	controlPlane ControlPlane
	serviceKeys  ServiceKeyManager
	oauthAuth    Authenticator
	staticKeys   map[string]struct{}
	requireAuth  bool
	handler      http.Handler
	tracer       *trace.Tracer
}

func NewDiscoveryServer(discovery Discovery) *DiscoveryServer {
	idBytes := make([]byte, 32)
	rand.Read(idBytes)
	id := hex.EncodeToString(idBytes)

	s := &DiscoveryServer{
		id:         id,
		discovery:  discovery,
		staticKeys: make(map[string]struct{}),
	}
	if cp, ok := discovery.(ControlPlane); ok {
		s.controlPlane = cp
	}
	if skm, ok := discovery.(ServiceKeyManager); ok {
		s.serviceKeys = skm
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

	h := s.authMiddleware(mux)
	return trace.Middleware("discovery", s.tracer)(h)
}

func (s *DiscoveryServer) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /id is allowed unauthenticated for basic health probing
		if r.URL.Path == "/id" {
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
	var reg ServiceRegistration
	if err := json.NewDecoder(r.Body).Decode(&reg); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

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
	if hasCaller && caller.Kind == CallerKindUser {
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
	if (s.oauthAuth != nil || s.requireAuth) && caller.Kind != CallerKindUser {
		http.Error(w, "Only authenticated users can create service keys", http.StatusForbidden)
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
	if (s.oauthAuth != nil || s.requireAuth) && caller.Kind != CallerKindUser {
		http.Error(w, "Only authenticated users can list service keys", http.StatusForbidden)
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
	if (s.oauthAuth != nil || s.requireAuth) && caller.Kind != CallerKindUser {
		http.Error(w, "Only authenticated users can revoke service keys", http.StatusForbidden)
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
