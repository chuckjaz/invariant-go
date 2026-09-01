package review

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"invariant/internal/discovery"
	"invariant/internal/identity"
	repoid "invariant/internal/repository/identity"
	"invariant/internal/trace"
)

// Assert that Server implements identity.Identity
var _ identity.Identity = (*Server)(nil)

// Server exposes a review.Service over HTTP REST API.
type Server struct {
	id        string
	svc       Service
	discovery discovery.Discovery
	handler   http.Handler
	tracer    *trace.Tracer
}

// NewServer creates a new HTTP server wrapping a review.Service.
func NewServer(svc Service) *Server {
	var id string
	if idSvc, ok := svc.(identity.Identity); ok {
		id = idSvc.ID()
	} else {
		b := make([]byte, 32)
		rand.Read(b)
		id = hex.EncodeToString(b)
	}

	s := &Server{
		id:  id,
		svc: svc,
	}
	s.handler = s.Handler()
	return s
}

// ID returns the unique 32-byte hex ID of the review service instance.
func (s *Server) ID() string {
	return s.id
}

// WithDiscovery attaches a Discovery client to the server.
func (s *Server) WithDiscovery(d discovery.Discovery) *Server {
	s.discovery = d
	return s
}

// WithTracer attaches a Tracer to the review server.
func (s *Server) WithTracer(t *trace.Tracer) *Server {
	s.tracer = t
	s.handler = s.Handler()
	return s
}

// Register registers this review service instance with the discovery service.
func (s *Server) Register(ctx context.Context, disc discovery.Discovery, advertiseAddr string, port int, tags []string) error {
	return discovery.AdvertiseAndRegister(ctx, disc, s.ID(), advertiseAddr, port, []string{"review-v1"}, tags)
}

// Handler returns the HTTP handler with all review service routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /id", s.handleGetID)
	mux.HandleFunc("POST /reviews/request", s.handleRequest)
	mux.HandleFunc("GET /reviews/", s.handleGet)
	mux.HandleFunc("POST /reviews/", s.handlePostAction)
	return trace.Middleware("review", s.tracer)(mux)
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *Server) handleGetID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(s.id))
}

func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		RepoName   string          `json:"repoName"`
		BranchName string          `json:"branchName"`
		Author     repoid.Identity `json:"author"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rec, err := s.svc.RequestReview(r.Context(), req.RepoName, req.BranchName, req.Author)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rec)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/reviews/")
	token = strings.TrimSuffix(token, "/")
	if token == "" {
		http.Error(w, "Missing review token", http.StatusBadRequest)
		return
	}
	rec, err := s.svc.GetReview(r.Context(), token)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rec)
}

func (s *Server) handlePostAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/reviews/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		http.Error(w, "Invalid review action path", http.StatusBadRequest)
		return
	}
	token := parts[0]
	action := parts[1]

	switch action {
	case "start":
		var req struct {
			Reviewer repoid.Identity `json:"reviewer"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if err := s.svc.StartReview(r.Context(), token, req.Reviewer); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)

	case "comments":
		var req struct {
			Comments []ReviewComment `json:"comments"`
			Author   repoid.Identity `json:"author"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.svc.AddComments(r.Context(), token, req.Comments, req.Author); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)

	case "approve":
		var req struct {
			Reviewer repoid.Identity `json:"reviewer"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if err := s.svc.ApproveReview(r.Context(), token, req.Reviewer); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)

	case "reject":
		var req struct {
			Reviewer repoid.Identity `json:"reviewer"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if err := s.svc.RejectReview(r.Context(), token, req.Reviewer); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)

	case "abandon":
		var req struct {
			Author repoid.Identity `json:"author"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if err := s.svc.AbandonReview(r.Context(), token, req.Author); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)

	default:
		http.Error(w, "Unknown review action", http.StatusNotFound)
	}
}
