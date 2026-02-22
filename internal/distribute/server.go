package distribute

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
)

// DistributeServer provides an HTTP interface for the Distribute service.
type DistributeServer struct {
	id         string
	distribute Distribute
	handler    http.Handler
}

// NewDistributeServer creates a new Distribute HTTP server.
func NewDistributeServer(distribute Distribute) *DistributeServer {
	idBytes := make([]byte, 32)
	rand.Read(idBytes)
	id := hex.EncodeToString(idBytes)

	s := &DistributeServer{
		id:         id,
		distribute: distribute,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /id", s.handleGetID)
	mux.HandleFunc("PUT /register/{id}", s.handleRegister)
	mux.HandleFunc("PUT /has/{id}", s.handleHas)

	s.handler = mux
	return s
}

// ServeHTTP implements the http.Handler interface.
func (s *DistributeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *DistributeServer) handleGetID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(s.id))
}

func (s *DistributeServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Bad Request: missing id", http.StatusBadRequest)
		return
	}

	if err := s.distribute.Register(id); err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *DistributeServer) handleHas(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Bad Request: missing id", http.StatusBadRequest)
		return
	}

	var req HasRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request: invalid JSON", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if err := s.distribute.Has(id, req.Addresses); err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
