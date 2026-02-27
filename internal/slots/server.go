// Package slots provides the HTTP server for the slots service.
package slots

import (
	"encoding/json"
	"net/http"
)

// Server wraps a Slots implementation and provides HTTP endpoints.
type Server struct {
	slots Slots
}

// NewServer creates a new Slots HTTP server.
func NewServer(slots Slots) *Server {
	return &Server{
		slots: slots,
	}
}

// Handler returns the http.Handler for the slots service endpoints.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /id", s.handleGetID)
	mux.HandleFunc("GET /{id}", s.handleGetSlot)
	mux.HandleFunc("PUT /{id}", s.handleUpdateSlot)
	mux.HandleFunc("POST /{id}", s.handleCreateSlot)

	return mux
}

// ServeHTTP implements the http.Handler interface.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Handler().ServeHTTP(w, r)
}

func (s *Server) handleGetID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(s.slots.ID()))
}

func (s *Server) handleGetSlot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Bad Request: missing id", http.StatusBadRequest)
		return
	}

	addr, err := s.slots.Get(id)
	if err != nil {
		if err == ErrSlotNotFound {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(addr))
}

func (s *Server) handleUpdateSlot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Bad Request: missing id", http.StatusBadRequest)
		return
	}

	var reqBody SlotUpdate
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "Bad Request: valid JSON expected", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if err := s.slots.Update(id, reqBody.Address, reqBody.PreviousAddress); err != nil {
		if err == ErrSlotNotFound {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		if err == ErrConflict {
			http.Error(w, "Conflict: previous address does not match", http.StatusConflict)
			return
		}
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleCreateSlot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Bad Request: missing id", http.StatusBadRequest)
		return
	}

	var reqBody SlotRegistration
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "Bad Request: valid JSON expected", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if err := s.slots.Create(id, reqBody.Address); err != nil {
		if err == ErrSlotExists {
			http.Error(w, "Conflict: slot already exists", http.StatusConflict)
			return
		}
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
