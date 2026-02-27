// Package slots provides the HTTP server for the slots service.
package slots

import (
	"encoding/json"
	"net/http"
	"time"
)

// Server wraps a Slots implementation and provides HTTP endpoints.
type Server struct {
	id    string
	slots Slots
}

// NewServer creates a new Slots HTTP server.
func NewServer(slots Slots) *Server {
	return &Server{
		id:    slots.ID(),
		slots: slots,
	}
}

// HasClient represents a client that can notify a service about known items.
type HasClient interface {
	Has(id string, addresses []string) error
}

// StartHasNotification starts a background goroutine that sends all stored
// slot IDs to the provided Has clients in batches.
func (s *Server) StartHasNotification(clients []HasClient, batchSize int, batchDuration time.Duration) {
	if len(clients) == 0 {
		return
	}
	if batchSize <= 0 {
		batchSize = 10000
	}
	if batchDuration <= 0 {
		batchDuration = 1 * time.Second
	}

	go func() {
		// 1. Send initial batch of all existing slots
		for batch := range s.slots.List(batchSize) {
			for _, client := range clients {
				_ = client.Has(s.id, batch)
			}
		}

		// 2. Listen for new slots and send them in batches
		sub := s.slots.Subscribe()
		var currentBatch []string
		ticker := time.NewTicker(batchDuration)
		defer ticker.Stop()

		sendBatch := func() {
			if len(currentBatch) == 0 {
				return
			}
			for _, client := range clients {
				_ = client.Has(s.id, currentBatch)
			}
			currentBatch = nil
		}

		for {
			select {
			case addr, ok := <-sub:
				if !ok {
					return
				}
				currentBatch = append(currentBatch, addr)
				if len(currentBatch) >= batchSize {
					sendBatch()
					ticker.Reset(batchDuration)
				}
			case <-ticker.C:
				sendBatch()
			}
		}
	}()
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
