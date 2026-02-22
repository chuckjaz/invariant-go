package discovery

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
)

type DiscoveryServer struct {
	id        string
	discovery Discovery
}

func NewDiscoveryServer(discovery Discovery) *DiscoveryServer {
	idBytes := make([]byte, 32)
	rand.Read(idBytes)
	id := hex.EncodeToString(idBytes)

	return &DiscoveryServer{
		id:        id,
		discovery: discovery,
	}
}

func (s *DiscoveryServer) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /id", s.handleGetID)
	mux.HandleFunc("GET /{id}", s.handleGet)
	mux.HandleFunc("GET /", s.handleFind)
	mux.HandleFunc("PUT /{id}", s.handlePut)

	return mux
}

func (s *DiscoveryServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Handler().ServeHTTP(w, r)
}

func (s *DiscoveryServer) handleGetID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(s.id))
}

func (s *DiscoveryServer) handleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	desc, ok := s.discovery.Get(id)
	if !ok {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(desc)
}

func (s *DiscoveryServer) handleFind(w http.ResponseWriter, r *http.Request) {
	protocol := r.URL.Query().Get("protocol")
	if protocol == "" {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	countStr := r.URL.Query().Get("count")
	count := 1
	if countStr != "" {
		parsedCount, err := strconv.Atoi(countStr)
		if err == nil && parsedCount > 0 {
			count = parsedCount
		}
	}

	descs, err := s.discovery.Find(protocol, count)
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

	if err := s.discovery.Register(reg); err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
