package commit

import (
	"encoding/json"
	"net/http"
	"strings"

	"invariant/internal/content"
)

// Server exposes a commit.Service over HTTP REST API.
type Server struct {
	svc     Service
	handler http.Handler
}

// NewServer creates a new HTTP server wrapping a commit.Service.
func NewServer(svc Service) *Server {
	s := &Server{svc: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/commit", s.handleCommit)
	mux.HandleFunc("/api/v1/commit/", s.handleGetCommit)
	mux.HandleFunc("/api/v1/history", s.handleHistory)
	mux.HandleFunc("/api/v1/diff", s.handleDiff)
	mux.HandleFunc("/api/v1/sync", s.handleSync)
	mux.HandleFunc("/api/v1/submit", s.handleSubmit)
	mux.HandleFunc("/api/v1/blame", s.handleBlame)
	mux.HandleFunc("/api/v1/bisect", s.handleBisect)
	mux.HandleFunc("/api/v1/rebase", s.handleRebase)
	s.handler = mux
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c, hash, err := s.svc.CreateCommit(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := struct {
		Commit *Commit `json:"commit"`
		Hash   string  `json:"hash"`
	}{
		Commit: c,
		Hash:   hash,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleGetCommit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	hash := strings.TrimPrefix(r.URL.Path, "/api/v1/commit/")
	if hash == "" {
		http.Error(w, "Missing commit hash", http.StatusBadRequest)
		return
	}
	c, err := s.svc.GetCommit(r.Context(), hash)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(c)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	head := r.URL.Query().Get("head")
	spineOnly := r.URL.Query().Get("spine") == "true"
	path := r.URL.Query().Get("path")

	commits, hashes, err := s.svc.GetHistory(r.Context(), head, spineOnly, path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := struct {
		Commits []*Commit `json:"commits"`
		Hashes  []string  `json:"hashes"`
	}{
		Commits: commits,
		Hashes:  hashes,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FromTree struct {
			Address string `json:"address"`
		} `json:"fromTree"`
		ToTree struct {
			Address string `json:"address"`
		} `json:"toTree"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	fromLink := content.ContentLink{Address: req.FromTree.Address}
	toLink := content.ContentLink{Address: req.ToTree.Address}

	diff, stat, err := s.svc.ComputeDiff(r.Context(), fromLink, toLink)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := struct {
		Diff string   `json:"diff"`
		Stat DiffStat `json:"stat"`
	}{
		Diff: diff,
		Stat: stat,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepoName     string `json:"repoName"`
		ChangeBranch string `json:"changeBranch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	newHead, conflicts, err := s.svc.SyncBranch(r.Context(), req.RepoName, req.ChangeBranch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := struct {
		NewHead   string   `json:"newHead"`
		Conflicts []string `json:"conflicts,omitempty"`
	}{
		NewHead:   newHead,
		Conflicts: conflicts,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var req SubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp, err := s.svc.SubmitChange(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleBlame(w http.ResponseWriter, r *http.Request) {
	commit := r.URL.Query().Get("commit")
	file := r.URL.Query().Get("file")
	lines, err := s.svc.Blame(r.Context(), commit, file)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(lines)
}

func (s *Server) handleBisect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Good []string `json:"good"`
		Bad  []string `json:"bad"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	candidate, remaining, err := s.svc.Bisect(r.Context(), req.Good, req.Bad)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := struct {
		Candidate string `json:"candidate"`
		Remaining int    `json:"remaining"`
	}{
		Candidate: candidate,
		Remaining: remaining,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleRebase(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepoName     string         `json:"repoName"`
		ChangeBranch string         `json:"changeBranch"`
		BaseCommit   string         `json:"baseCommit"`
		Plan         []RebaseAction `json:"plan"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	newHead, err := s.svc.InteractiveRebase(r.Context(), req.RepoName, req.ChangeBranch, req.BaseCommit, req.Plan)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := struct {
		NewHead string `json:"newHead"`
	}{
		NewHead: newHead,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
