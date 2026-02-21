package storage

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
)

type StorageServer struct {
	id      string
	storage Storage
}

func NewStorageServer(storage Storage) *StorageServer {
	idBytes := make([]byte, 32)
	rand.Read(idBytes)
	id := hex.EncodeToString(idBytes)

	return &StorageServer{
		id:      id,
		storage: storage,
	}
}

func (s *StorageServer) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /id", s.handleGetID)

	mux.HandleFunc("POST /storage/", s.handlePost)
	mux.HandleFunc("GET /storage/{address}", s.handleGet)
	mux.HandleFunc("HEAD /storage/{address}", s.handleHead)
	mux.HandleFunc("PUT /storage/{address}", s.handlePut)

	mux.HandleFunc("POST /storage/fetch", s.handleFetch)
	mux.HandleFunc("HEAD /storage/fetch", s.handleFetch)

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
	w.WriteHeader(http.StatusNotFound)
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
