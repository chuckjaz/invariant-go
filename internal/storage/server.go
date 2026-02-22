package storage

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"invariant/internal/discovery"
	"invariant/internal/identity"
	"io"
	"net/http"
	"strconv"
)

type StorageServer struct {
	id        string
	storage   Storage
	discovery discovery.Discovery
}

func NewStorageServer(storage Storage) *StorageServer {
	var id string
	if idStorage, ok := storage.(identity.Provider); ok {
		id = idStorage.ID()
	} else {
		idBytes := make([]byte, 32)
		rand.Read(idBytes)
		id = hex.EncodeToString(idBytes)
	}

	return &StorageServer{
		id:      id,
		storage: storage,
	}
}

// HasClient represents a client that can notify a service about known blocks.
type HasClient interface {
	Has(storageID string, addresses []string) error
}

// WithDiscovery sets the discovery client used by the storage server
// to locate other storage nodes for fetching operations.
func (s *StorageServer) WithDiscovery(d discovery.Discovery) *StorageServer {
	s.discovery = d
	return s
}

// StartHasNotification starts a background goroutine that sends all stored
// block addresses to the provided Has clients in batches.
func (s *StorageServer) StartHasNotification(clients []HasClient, batchSize int) {
	if len(clients) == 0 {
		return
	}
	if batchSize <= 0 {
		batchSize = 10000
	}

	go func() {
		for batch := range s.storage.List(batchSize) {
			for _, client := range clients {
				// Ignore errors in background notification
				_ = client.Has(s.id, batch)
			}
		}
	}()
}

func (s *StorageServer) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /id", s.handleGetID)
	mux.HandleFunc("HEAD /id", s.handleGetID)

	mux.HandleFunc("POST /{$}", s.handlePost)

	mux.HandleFunc("POST /fetch", s.handleFetch)
	mux.HandleFunc("HEAD /fetch", s.handleFetch)

	mux.HandleFunc("/{address}", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.handleGet(w, r)
		case http.MethodHead:
			s.handleHead(w, r)
		case http.MethodPut:
			s.handlePut(w, r)
		default:
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	})

	return mux
}

func (s *StorageServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Handler().ServeHTTP(w, r)
}

func (s *StorageServer) handleGetID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(s.id))
}

func (s *StorageServer) handleFetch(w http.ResponseWriter, r *http.Request) {
	if s.discovery == nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var reqBody StorageFetchRequest
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if reqBody.Address == "" || reqBody.Container == "" {
		http.Error(w, "Bad Request: missing address or container", http.StatusBadRequest)
		return
	}

	// Local optimization: if we already have it, just return success
	if s.storage.Has(reqBody.Address) {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Lookup the container ID via Discovery to get its HTTP address
	desc, ok := s.discovery.Get(reqBody.Container)
	if !ok {
		http.Error(w, "Bad Gateway: container not found in discovery", http.StatusBadGateway)
		return
	}

	// Create a storage client pointing at the remote node
	remoteClient := NewClient(desc.Address, nil)

	// Stream the data directly from the remote node to our local storage
	data, ok := remoteClient.Get(reqBody.Address)
	if !ok {
		http.Error(w, "Bad Gateway: failed to get block from remote", http.StatusBadGateway)
		return
	}
	defer data.Close()

	success, err := s.storage.StoreAt(reqBody.Address, data)
	if err != nil || !success {
		http.Error(w, "Internal Server Error: failed to store fetched block", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *StorageServer) handlePost(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	address, err := s.storage.Store(r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(address))
}

func (s *StorageServer) handlePut(w http.ResponseWriter, r *http.Request) {
	address := r.PathValue("address")
	defer r.Body.Close()

	success, err := s.storage.StoreAt(address, r.Body)
	if err != nil || !success {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(address))
}

func (s *StorageServer) handleGet(w http.ResponseWriter, r *http.Request) {
	address := r.PathValue("address")
	data, ok := s.storage.Get(address)
	if !ok {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	defer data.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "immutable")
	w.Header().Set("ETag", address)

	size, ok := s.storage.Size(address)
	if ok {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}

	w.WriteHeader(http.StatusOK)
	io.Copy(w, data)
}

func (s *StorageServer) handleHead(w http.ResponseWriter, r *http.Request) {
	address := r.PathValue("address")
	size, ok := s.storage.Size(address)
	if !ok {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("ETag", address)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))

	w.WriteHeader(http.StatusOK)
}
