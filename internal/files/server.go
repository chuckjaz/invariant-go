package files

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"invariant/internal/content"
	"invariant/internal/filetree"
)

// Server exposes a Files interface over HTTP
type Server struct {
	files Files
}

// NewServer creates a new HTTP server wrapper for the Files interface
func NewServer(files Files) *Server {
	return &Server{files: files}
}

// Handler returns the http.Handler for the files service
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("PUT /remove/{node}/{name}", s.handleRemove)
	mux.HandleFunc("POST /rename/{node}/{name}", s.handleRename)
	mux.HandleFunc("PUT /link/{node}/{name}", s.handleLink)

	mux.HandleFunc("PUT /{node}/{name}", s.handlePutEntry)
	mux.HandleFunc("GET /lookup/{node}/{name}", s.handleLookup)

	mux.HandleFunc("GET /file/{node}", s.handleGetFile)
	mux.HandleFunc("POST /file/{node}", s.handlePostFile)

	mux.HandleFunc("GET /directory/{node}", s.handleGetDirectory)

	mux.HandleFunc("GET /attributes/{node}", s.handleGetAttributes)
	mux.HandleFunc("POST /attributes/{node}", s.handleSetAttributes)

	mux.HandleFunc("GET /content/{node}", s.handleGetContent)
	mux.HandleFunc("GET /info/{node}", s.handleGetInfo)

	mux.HandleFunc("PUT /sync", s.handleSync)

	return mux
}

func parseNodeID(nodeStr string) (uint64, error) {
	nodeStr = strings.TrimPrefix(nodeStr, "/")
	nodeID, err := strconv.ParseUint(nodeStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid node ID: %v", err)
	}
	return nodeID, nil
}

func (s *Server) handlePutEntry(w http.ResponseWriter, r *http.Request) {
	parentID, err := parseNodeID(r.PathValue("node"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	name := r.PathValue("name")

	kindStr := r.URL.Query().Get("kind")
	if kindStr == "" {
		kindStr = string(filetree.FileKind)
	}
	kind := filetree.EntryKind(kindStr)

	var link *content.ContentLink
	contentParam := r.URL.Query().Get("content")
	if contentParam != "" {
		link = &content.ContentLink{}
		if err := json.Unmarshal([]byte(contentParam), link); err != nil {
			http.Error(w, "invalid content link", http.StatusBadRequest)
			return
		}
	}

	target := r.URL.Query().Get("target")

	err = s.files.CreateEntry(r.Context(), parentID, name, kind, target, link, r.Body)
	if err != nil {
		if err.Error() == "file system is read-only" {
			http.Error(w, err.Error(), http.StatusForbidden)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleGetFile(w http.ResponseWriter, r *http.Request) {
	nodeID, err := parseNodeID(r.PathValue("node"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	offsetStr := r.URL.Query().Get("offset")
	var offset int64
	if offsetStr != "" {
		offset, _ = strconv.ParseInt(offsetStr, 10, 64)
	}

	lengthStr := r.URL.Query().Get("length")
	var length int64
	if lengthStr != "" {
		length, _ = strconv.ParseInt(lengthStr, 10, 64)
	}

	reader, err := s.files.ReadFile(r.Context(), nodeID, offset, length)
	if err != nil {
		if err.Error() == "invalid file node" {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	defer reader.Close()

	if _, err := io.Copy(w, reader); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handlePostFile(w http.ResponseWriter, r *http.Request) {
	nodeID, err := parseNodeID(r.PathValue("node"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	offsetStr := r.URL.Query().Get("offset")
	var offset int64
	if offsetStr != "" {
		offset, _ = strconv.ParseInt(offsetStr, 10, 64)
	}

	appendFlag := r.URL.Query().Get("append") == "true"

	err = s.files.WriteFile(r.Context(), nodeID, offset, appendFlag, r.Body)
	if err != nil {
		if err.Error() == "file system is read-only" {
			http.Error(w, err.Error(), http.StatusForbidden)
		} else if err.Error() == "invalid file node" {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleGetDirectory(w http.ResponseWriter, r *http.Request) {
	nodeID, err := parseNodeID(r.PathValue("node"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	offsetStr := r.URL.Query().Get("offset")
	var offset int64
	if offsetStr != "" {
		offset, _ = strconv.ParseInt(offsetStr, 10, 64)
	}

	lengthStr := r.URL.Query().Get("length")
	var length int64
	if lengthStr != "" {
		length, _ = strconv.ParseInt(lengthStr, 10, 64)
	}

	entries, err := s.files.ReadDirectory(r.Context(), nodeID, offset, length)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// For simplicity in pagination here, applying offset/length filtering memory-side
	// based on the interface returned list. Interface could do this natively.
	if offset > 0 && offset < int64(len(entries)) {
		entries = entries[offset:]
	} else if offset >= int64(len(entries)) {
		entries = filetree.Directory{}
	}

	if length > 0 && length < int64(len(entries)) {
		entries = entries[:length]
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

func (s *Server) handleGetAttributes(w http.ResponseWriter, r *http.Request) {
	nodeID, err := parseNodeID(r.PathValue("node"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	attrs, err := s.files.GetAttributes(r.Context(), nodeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(attrs)
}

func (s *Server) handleSetAttributes(w http.ResponseWriter, r *http.Request) {
	nodeID, err := parseNodeID(r.PathValue("node"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var attrs EntryAttributes
	if err := json.NewDecoder(r.Body).Decode(&attrs); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	newAttrs, err := s.files.SetAttributes(r.Context(), nodeID, attrs)
	if err != nil {
		if err.Error() == "file system is read-only" {
			http.Error(w, err.Error(), http.StatusForbidden)
		} else {
			http.Error(w, err.Error(), http.StatusNotFound)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(newAttrs)
}

func (s *Server) handleGetContent(w http.ResponseWriter, r *http.Request) {
	nodeID, err := parseNodeID(r.PathValue("node"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	link, err := s.files.GetContent(r.Context(), nodeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(link)
}

func (s *Server) handleGetInfo(w http.ResponseWriter, r *http.Request) {
	nodeID, err := parseNodeID(r.PathValue("node"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	info, err := s.files.GetInfo(r.Context(), nodeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

func (s *Server) handleLookup(w http.ResponseWriter, r *http.Request) {
	parentID, err := parseNodeID(r.PathValue("node"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	name := r.PathValue("name")

	info, err := s.files.Lookup(r.Context(), parentID, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	parentID, err := parseNodeID(r.PathValue("node"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	name := r.PathValue("name")

	err = s.files.Remove(r.Context(), parentID, name)
	if err != nil {
		if err.Error() == "file system is read-only" {
			http.Error(w, err.Error(), http.StatusForbidden)
		} else {
			http.Error(w, err.Error(), http.StatusNotFound)
		}
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	parentID, err := parseNodeID(r.PathValue("node"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	oldName := r.PathValue("name")
	newName := r.URL.Query().Get("name")
	if newName == "" {
		http.Error(w, "name query parameter is required", http.StatusBadRequest)
		return
	}

	newParentID := parentID
	newDirStr := r.URL.Query().Get("directory")
	if newDirStr != "" {
		id, err := parseNodeID(newDirStr)
		if err != nil {
			http.Error(w, "invalid directory query parameter", http.StatusBadRequest)
			return
		}
		newParentID = id
	}

	err = s.files.Rename(r.Context(), parentID, oldName, newParentID, newName)
	if err != nil {
		if err.Error() == "file system is read-only" {
			http.Error(w, err.Error(), http.StatusForbidden)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleLink(w http.ResponseWriter, r *http.Request) {
	parentID, err := parseNodeID(r.PathValue("node"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	name := r.PathValue("name")
	targetStr := r.URL.Query().Get("node")
	if targetStr == "" {
		http.Error(w, "node query parameter is required", http.StatusBadRequest)
		return
	}

	targetID, err := parseNodeID(targetStr)
	if err != nil {
		http.Error(w, "invalid node parameter", http.StatusBadRequest)
		return
	}

	err = s.files.Link(r.Context(), parentID, name, targetID)
	if err != nil {
		if err.Error() == "file system is read-only" {
			http.Error(w, err.Error(), http.StatusForbidden)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	nodeID := uint64(1) // Default to root
	nodeStr := r.URL.Query().Get("node")
	if nodeStr != "" {
		id, err := parseNodeID(nodeStr)
		if err != nil {
			http.Error(w, "invalid node parameter", http.StatusBadRequest)
			return
		}
		nodeID = id
	}

	wait := r.URL.Query().Get("wait") != "false"

	err := s.files.Sync(context.Background(), nodeID, wait)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
