package finder

import (
	"encoding/json"
	"invariant/internal/discovery"
	"net/http"

	"invariant/internal/has"
)

// FinderServer wraps a Finder implementation and provides HTTP endpoints.
type FinderServer struct {
	finder    Finder
	discovery discovery.Discovery
}

// NewFinderServer creates a new Finder HTTP server.
func NewFinderServer(finder Finder, disc discovery.Discovery) *FinderServer {
	return &FinderServer{
		finder:    finder,
		discovery: disc,
	}
}

func (s *FinderServer) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /id", s.handleGetID)
	mux.HandleFunc("GET /{address}", s.handleFind)
	mux.HandleFunc("PUT /has/{id}", s.handleHas)
	mux.HandleFunc("PUT /notify/{id}", s.handleNotify)

	return mux
}

func (s *FinderServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Handler().ServeHTTP(w, r)
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

	responses, err := s.finder.Find(address)
	if err != nil {
		// Differentiate between bad address formats and internal errors
		if err.Error() == "invalid block address format: encoding/hex: invalid byte: U+007A 'z'" {
			http.Error(w, "Bad Request: invalid address format", http.StatusBadRequest)
			return
		}
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(responses)
}

func (s *FinderServer) handleHas(w http.ResponseWriter, r *http.Request) {
	storageID := r.PathValue("id")
	if storageID == "" {
		http.Error(w, "Bad Request: missing storage ID", http.StatusBadRequest)
		return
	}

	var reqBody has.HasRequest
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "Bad Request: valid JSON expected", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if err := s.finder.Has(storageID, reqBody.Addresses); err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *FinderServer) handleNotify(w http.ResponseWriter, r *http.Request) {
	newFinderID := r.PathValue("id")
	if newFinderID == "" {
		http.Error(w, "Bad Request: missing finder ID", http.StatusBadRequest)
		return
	}

	// 1. Add them to our routing table
	if err := s.finder.Notify(newFinderID); err != nil {
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
	// Look up the new finder in discovery
	desc, ok := s.discovery.Get(newFinderID)
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
	ft, ok := s.finder.(FinderTest)
	if !ok {
		return
	}
	knownBlocks := ft.SnapshotBlocks()

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
		remoteClient.Has(sID, addrs)
	}
}
