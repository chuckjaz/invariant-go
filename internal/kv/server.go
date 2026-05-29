package kv

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
)

type Server struct {
	store KeyValueStore
}

func NewServer(store KeyValueStore) *Server {
	return &Server{store: store}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /put", s.handlePut)
	mux.HandleFunc("GET /get", s.handleGet)
	mux.HandleFunc("GET /history", s.handleGetHistory)
	mux.HandleFunc("POST /batch_put", s.handleBatchPut)
	mux.HandleFunc("POST /batch_get", s.handleBatchGet)
	mux.HandleFunc("POST /batch_history", s.handleBatchGetHistory)

	return mux
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Handler().ServeHTTP(w, r)
}

func (s *Server) handlePut(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "Missing key", http.StatusBadRequest)
		return
	}

	value, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	seq, err := s.store.Put(r.Context(), key, value)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("X-Sequence", fmt.Sprint(seq))
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "Missing key", http.StatusBadRequest)
		return
	}

	val, seq, err := s.store.Get(r.Context(), key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("X-Sequence", fmt.Sprint(seq))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(val)
}

func (s *Server) handleBatchPut(w http.ResponseWriter, r *http.Request) {
	reader, err := r.MultipartReader()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	kvs := make(map[string][]byte)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		key := part.FormName()
		if key == "" {
			continue
		}

		val, err := io.ReadAll(part)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		kvs[key] = val
	}

	if batchStore, ok := s.store.(BatchKeyValueStore); ok {
		seq, err := batchStore.BatchPut(r.Context(), kvs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("X-Sequence", fmt.Sprint(seq))
		w.WriteHeader(http.StatusOK)
		return
	}

	var maxSeq uint64
	for key, val := range kvs {
		seq, err := s.store.Put(r.Context(), key, val)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if seq > maxSeq {
			maxSeq = seq
		}
	}

	w.Header().Set("X-Sequence", fmt.Sprint(maxSeq))
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleBatchGet(w http.ResponseWriter, r *http.Request) {
	var keys []string
	if err := json.NewDecoder(r.Body).Decode(&keys); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	mw := multipart.NewWriter(w)
	w.Header().Set("Content-Type", mw.FormDataContentType())

	if batchStore, ok := s.store.(BatchKeyValueStore); ok {
		results, err := batchStore.BatchGet(r.Context(), keys)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		for key, val := range results {
			h := make(textproto.MIMEHeader)
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"`, key))
			h.Set("X-Sequence", fmt.Sprint(val.Sequence))
			part, err := mw.CreatePart(h)
			if err != nil {
				continue
			}
			part.Write(val.Value)
		}
	} else {
		for _, key := range keys {
			val, seq, err := s.store.Get(r.Context(), key)
			if err == nil && val != nil {
				h := make(textproto.MIMEHeader)
				h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"`, key))
				h.Set("X-Sequence", fmt.Sprint(seq))
				part, err := mw.CreatePart(h)
				if err != nil {
					continue
				}
				part.Write(val)
			}
		}
	}

	if err := mw.Close(); err != nil {
		// Log error or ignore, headers already sent
	}
}

func parseUintQuery(r *http.Request, key string, def uint64) uint64 {
	valStr := r.URL.Query().Get(key)
	if valStr == "" {
		return def
	}
	val, err := strconv.ParseUint(valStr, 10, 64)
	if err != nil {
		return def
	}
	return val
}

func (s *Server) handleGetHistory(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "Missing key", http.StatusBadRequest)
		return
	}

	minSeq := parseUintQuery(r, "min", 0)
	maxSeq := parseUintQuery(r, "max", ^uint64(0))
	limit := int(parseUintQuery(r, "limit", 100))

	page, err := s.store.GetHistory(r.Context(), key, minSeq, maxSeq, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("X-Has-More", fmt.Sprint(page.HasMore))

	mw := multipart.NewWriter(w)
	w.Header().Set("Content-Type", mw.FormDataContentType())

	for _, val := range page.Values {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"`, key))
		h.Set("X-Sequence", fmt.Sprint(val.Sequence))
		part, err := mw.CreatePart(h)
		if err != nil {
			continue
		}
		part.Write(val.Value)
	}

	mw.Close()
}

func (s *Server) handleBatchGetHistory(w http.ResponseWriter, r *http.Request) {
	var keys []string
	if err := json.NewDecoder(r.Body).Decode(&keys); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	minSeq := parseUintQuery(r, "min", 0)
	maxSeq := parseUintQuery(r, "max", ^uint64(0))
	limit := int(parseUintQuery(r, "limit", 100))

	mw := multipart.NewWriter(w)
	w.Header().Set("Content-Type", mw.FormDataContentType())

	var results map[string]HistoryPage
	if batchStore, ok := s.store.(BatchKeyValueStore); ok {
		var err error
		results, err = batchStore.BatchGetHistory(r.Context(), keys, minSeq, maxSeq, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		results = make(map[string]HistoryPage)
		for _, key := range keys {
			page, err := s.store.GetHistory(r.Context(), key, minSeq, maxSeq, limit)
			if err == nil && len(page.Values) > 0 {
				results[key] = page
			}
		}
	}

	for key, page := range results {
		for i, val := range page.Values {
			h := make(textproto.MIMEHeader)
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"`, key))
			h.Set("X-Sequence", fmt.Sprint(val.Sequence))
			if i == 0 {
				h.Set("X-Has-More", fmt.Sprint(page.HasMore))
			}
			part, err := mw.CreatePart(h)
			if err != nil {
				continue
			}
			part.Write(val.Value)
		}
	}

	mw.Close()
}
