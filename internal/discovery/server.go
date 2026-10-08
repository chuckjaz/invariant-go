package discovery

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"invariant/internal/trace"
)

type DiscoveryServer struct {
	id           string
	discovery    Discovery
	controlPlane ControlPlane
	authToken    string
	handler      http.Handler
	tracer       *trace.Tracer
}

func NewDiscoveryServer(discovery Discovery) *DiscoveryServer {
	idBytes := make([]byte, 32)
	rand.Read(idBytes)
	id := hex.EncodeToString(idBytes)

	s := &DiscoveryServer{
		id:        id,
		discovery: discovery,
	}
	if cp, ok := discovery.(ControlPlane); ok {
		s.controlPlane = cp
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

// WithAuthToken sets the required bearer token for authenticating requests.
func (s *DiscoveryServer) WithAuthToken(token string) *DiscoveryServer {
	s.authToken = token
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

	var h http.Handler = mux
	if s.authToken != "" {
		h = s.authMiddleware(h)
	}

	return trace.Middleware("discovery", s.tracer)(h)
}

func (s *DiscoveryServer) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /id is allowed unauthenticated for basic health probing
		if r.URL.Path == "/id" {
			next.ServeHTTP(w, r)
			return
		}
		authHeader := r.Header.Get("Authorization")
		expected := "Bearer " + s.authToken
		if authHeader != expected && authHeader != s.authToken {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
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
