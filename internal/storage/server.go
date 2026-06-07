package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"invariant/internal/discovery"
	"invariant/internal/identity"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type StorageServer struct {
	id        string
	storage   Storage
	discovery discovery.Discovery
	cacheMu   sync.RWMutex
	devCache  map[string]discovery.ServiceDescription
}

func NewStorageServer(storage Storage) *StorageServer {
	var id string
	if idStorage, ok := storage.(identity.Identity); ok {
		id = idStorage.ID()
	} else {
		idBytes := make([]byte, 32)
		rand.Read(idBytes)
		id = hex.EncodeToString(idBytes)
	}

	return &StorageServer{
		id:       id,
		storage:  storage,
		devCache: make(map[string]discovery.ServiceDescription),
	}
}

// NotifyClient represents a client that can notify a service about known blocks.
type NotifyClient interface {
	Notify(storageID string, addresses []string) error
}

// WithDiscovery sets the discovery client used by the storage server
// to locate other storage nodes for fetching operations.
func (s *StorageServer) WithDiscovery(d discovery.Discovery) *StorageServer {
	s.discovery = d
	return s
}

// StartNotification starts a background goroutine that sends all stored
// block addresses to the provided Has clients in batches.
func (s *StorageServer) StartNotification(ctx context.Context, clients []NotifyClient, batchSize int, batchDuration time.Duration) {
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
		cStorage, ok := s.storage.(ControlledStorage)
		if !ok {
			return
		}

		// 1. Send initial batch of all existing blocks
		for batch := range cStorage.List(ctx, batchSize) {
			for _, client := range clients {
				_ = client.Notify(s.id, batch)
			}
		}

		// 2. Listen for new blocks and send them in batches
		sub := cStorage.Subscribe(ctx)
		var currentBatch []string
		ticker := time.NewTicker(batchDuration)
		defer ticker.Stop()

		sendBatch := func() {
			if len(currentBatch) == 0 {
				return
			}
			for _, client := range clients {
				_ = client.Notify(s.id, currentBatch)
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
					ticker.Reset(batchDuration) // reset the ticker so we don't send an empty batch right away
				}
			case <-ticker.C:
				sendBatch()
			}
		}
	}()
}

func (s *StorageServer) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /id", s.handleGetID)

	mux.HandleFunc("POST /{$}", s.handlePost)

	mux.HandleFunc("POST /batch_has", s.handleBatchHas)
	mux.HandleFunc("POST /batch_store", s.handleBatchStore)

	mux.HandleFunc("POST /fetch", s.handleFetch)
	mux.HandleFunc("HEAD /fetch", s.handleFetch)

	mux.HandleFunc("GET /{address}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			s.handleHead(w, r)
		} else {
			s.handleGet(w, r)
		}
	})
	mux.HandleFunc("PUT /{address}", s.handlePut)

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
	if s.storage.Has(r.Context(), reqBody.Address) {
		w.WriteHeader(http.StatusOK)
		return
	}

	s.cacheMu.RLock()
	desc, ok := s.devCache[reqBody.Container]
	s.cacheMu.RUnlock()

	if !ok {
		// Lookup the container ID via Discovery to get its HTTP address
		desc, ok = s.discovery.Get(r.Context(), reqBody.Container)
		if !ok {
			http.Error(w, "Bad Gateway: container not found in discovery", http.StatusBadGateway)
			return
		}
		s.cacheMu.Lock()
		s.devCache[reqBody.Container] = desc
		s.cacheMu.Unlock()
	}

	// Create a storage client pointing at the remote node
	remoteClient := NewClient(desc.Address, nil)

	// Stream the data directly from the remote node to our local storage
	data, ok := remoteClient.Get(r.Context(), reqBody.Address)
	if !ok {
		http.Error(w, "Bad Gateway: failed to get block from remote", http.StatusBadGateway)
		return
	}
	defer data.Close()

	success, err := s.storage.StoreAt(r.Context(), reqBody.Address, data)
	if err != nil || !success {
		http.Error(w, "Internal Server Error: failed to store fetched block", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *StorageServer) handlePost(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	address, err := s.storage.Store(r.Context(), r.Body)
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

	success, err := s.storage.StoreAt(r.Context(), address, r.Body)
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
	data, ok := s.storage.Get(r.Context(), address)
	if !ok {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	defer data.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "immutable")
	w.Header().Set("ETag", address)

	size, ok := s.storage.Size(r.Context(), address)
	if ok {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}

	w.WriteHeader(http.StatusOK)
	io.Copy(w, data)
}

func (s *StorageServer) handleHead(w http.ResponseWriter, r *http.Request) {
	address := r.PathValue("address")
	size, ok := s.storage.Size(r.Context(), address)
	if !ok {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("ETag", address)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))

	w.WriteHeader(http.StatusOK)
}

func (s *StorageServer) handleBatchHas(w http.ResponseWriter, r *http.Request) {
	var addresses []string
	if err := json.NewDecoder(r.Body).Decode(&addresses); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if batchStore, ok := s.storage.(BatchStorage); ok {
		missing, err := batchStore.BatchHas(r.Context(), addresses)
		if err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"missing": missing})
		return
	}

	var missing []string
	for _, addr := range addresses {
		if !s.storage.Has(r.Context(), addr) {
			missing = append(missing, addr)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if missing == nil {
		missing = []string{}
	}
	json.NewEncoder(w).Encode(map[string]any{"missing": missing})
}

func (s *StorageServer) handleBatchStore(w http.ResponseWriter, r *http.Request) {
	reader, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "Bad Request: not multipart", http.StatusBadRequest)
		return
	}

	blocks := make(map[string]io.Reader)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "Bad Request: error reading multipart", http.StatusBadRequest)
			return
		}
		addr := part.FormName()
		if addr == "" {
			continue
		}

		// In order to not block NextPart, we either need to read the part into memory,
		// or process it immediately. Since we need to pass a map to BatchStore,
		// and multipart parts cannot be read out of order, we should probably read into memory here if we want to pass a map of readers.
		// Alternatively, if the server just calls StoreAt sequentially, we can do it inline.

		// Since BatchStore takes a map, we buffer into memory.
		// For large files, batch_store should NOT be used. It's meant for many small files.
		data, err := io.ReadAll(part)
		if err != nil {
			http.Error(w, "Bad Request: error reading part", http.StatusBadRequest)
			return
		}
		blocks[addr] = bytes.NewReader(data)
	}

	if batchStore, ok := s.storage.(BatchStorage); ok {
		err := batchStore.BatchStore(r.Context(), blocks)
		if err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	} else {
		for addr, rdr := range blocks {
			success, err := s.storage.StoreAt(r.Context(), addr, rdr)
			if err != nil || !success {
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
		}
	}

	w.WriteHeader(http.StatusOK)
}
