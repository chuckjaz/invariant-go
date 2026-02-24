package names

import (
	"encoding/json"
	"net/http"
	"strings"
)

type NamesServer struct {
	names Names
}

func NewNamesServer(names Names) *NamesServer {
	return &NamesServer{
		names: names,
	}
}

func (s *NamesServer) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{name}", s.handleGet)
	mux.HandleFunc("PUT /{name}", s.handlePut)
	mux.HandleFunc("DELETE /{name}", s.handleDelete)

	return mux
}

func (s *NamesServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Handler().ServeHTTP(w, r)
}

func (s *NamesServer) handleGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	entry, err := s.names.Get(name)
	if err == ErrNotFound {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("ETag", entry.Value)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(entry); err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func (s *NamesServer) handlePut(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	value := r.URL.Query().Get("value")
	tokensStr := r.URL.Query().Get("tokens")

	if value == "" {
		http.Error(w, "Bad Request: missing value", http.StatusBadRequest)
		return
	}

	if tokensStr == "" {
		http.Error(w, "Bad Request: missing tokens", http.StatusBadRequest)
		return
	}

	tokens := strings.Split(tokensStr, ",")

	// Proceed with normal Put, ETag precondition is only specified for DELETE in the protocol.

	err := s.names.Put(name, value, tokens)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("ETag", value)
	w.WriteHeader(http.StatusOK)
}

func (s *NamesServer) handleDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	expectedValue := r.Header.Get("If-Match")

	err := s.names.Delete(name, expectedValue)
	if err == ErrNotFound {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	} else if err == ErrPreconditionFailed {
		http.Error(w, "Precondition Failed", http.StatusPreconditionFailed)
		return
	} else if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("ETag", expectedValue)
	w.WriteHeader(http.StatusOK) // Or 204 No Content
}
