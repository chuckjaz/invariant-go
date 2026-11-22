package finder

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/chuckjaz/invariant-go/internal/discovery"
	"github.com/chuckjaz/invariant-go/internal/notify"
	"github.com/chuckjaz/invariant-go/internal/trace"
)

// FinderServer wraps a LocalFinder implementation and provides HTTP endpoints.
type FinderServer struct {
	finder    LocalFinder
	discovery discovery.Discovery
	handler   http.Handler
	tracer    *trace.Tracer
}

// NewFinderServer creates a new Finder HTTP server.
func NewFinderServer(finder LocalFinder, disc discovery.Discovery) *FinderServer {
	s := &FinderServer{
		finder:    finder,
		discovery: disc,
	}
	s.handler = s.Handler()
	return s
}

// WithTracer attaches a Tracer to the finder server.
func (s *FinderServer) WithTracer(t *trace.Tracer) *FinderServer {
	s.tracer = t
	s.handler = s.Handler()
	return s
}

func (s *FinderServer) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /id", s.handleGetID)
	mux.HandleFunc("GET /{address}", s.handleFind)
	mux.HandleFunc("PUT /notify/{id}", s.handleNotify)
	mux.HandleFunc("PUT /peer/{id}", s.handlePeer)

	return trace.Middleware("finder", s.tracer)(mux)
}

func (s *FinderServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *FinderServer) handleGetID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(s.finder.ID()))
}

func (s *FinderServer) handleFind(w http.ResponseWriter, r *http.Request) {
	address := r.PathValue("address")
	if address == "" {
		http.Error(w, "Bad Request: missing address", http.StatusBadRequest)
		return
	}

	responses, err := s.finder.Find(r.Context(), address)
	if err != nil {
		if errors.Is(err, ErrInvalidAddress) {
			http.Error(w, "Bad Request: invalid address format", http.StatusBadRequest)
			return
		}
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(responses)
}

func (s *FinderServer) handleNotify(w http.ResponseWriter, r *http.Request) {
	storageID := r.PathValue("id")
	if storageID == "" {
		http.Error(w, "Bad Request: missing storage ID", http.StatusBadRequest)
		return
	}

	var reqBody notify.NotifyRequest
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "Bad Request: valid JSON expected", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if err := s.finder.Notify(r.Context(), storageID, reqBody.Addresses); err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *FinderServer) handlePeer(w http.ResponseWriter, r *http.Request) {
	newFinderID := r.PathValue("id")
	if newFinderID == "" {
		http.Error(w, "Bad Request: missing finder ID", http.StatusBadRequest)
		return
	}

	// 1. Add them to our routing table
	if err := s.finder.Peer(r.Context(), newFinderID); err != nil {
		http.Error(w, "Bad Request: invalid finder ID", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)

	// 2. We need to tell the new finder about blocks we know that are closer to it than to us.
	// We do this in a goroutine to not block the response of their ping.
	if s.discovery != nil {
		go s.pushBlocksToCloserFinder(newFinderID)
	}
}

func (s *FinderServer) pushBlocksToCloserFinder(newFinderID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Look up the new finder in discovery
	desc, ok := s.discovery.Get(ctx, newFinderID)
	if !ok {
		return // Discovery doesn't know about them, can't push
	}

	// Create a client to talk to the new finder
	remoteClient := NewClient(desc.Address, nil)

	// Parse IDs for distance calculation
	localNodeID, err := ParseNodeID(s.finder.ID())
	if err != nil {
		return
	}
	remoteNodeID, err := ParseNodeID(newFinderID)
	if err != nil {
		return
	}

	// Iterate through our knowledge base
	knownBlocks := s.finder.SnapshotBlocks()

	// Batch blocks by storage ID
	// storageID -> list of addresses
	pushMap := make(map[string][]string)

	for blockAddr, storageIDs := range knownBlocks {
		blockNodeID, err := ParseNodeID(blockAddr)
		if err != nil {
			continue
		}

		// Kademlia: if remote is closer to the block than we are, tell them
		if remoteNodeID.Less(localNodeID, blockNodeID) {
			for _, sID := range storageIDs {
				pushMap[sID] = append(pushMap[sID], blockAddr)
			}
		}
	}

	// Send batches to the new finder
	for sID, addrs := range pushMap {
		if err := remoteClient.Notify(ctx, sID, addrs); err != nil {
			log.Printf("finder: notify peer %s for storage %s failed: %v", newFinderID, sID, err)
		}
	}
}
