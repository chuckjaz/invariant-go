package review

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"invariant/internal/identity"
	"invariant/internal/names"
	repoid "invariant/internal/repository/identity"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// Assert that LocalService implements Service and identity.Identity
var _ Service = (*LocalService)(nil)
var _ identity.Identity = (*LocalService)(nil)

// LocalService manages review lifecycles, comment trees, and approvals directly in CAS and Slots.
type LocalService struct {
	id          string
	store       storage.Storage
	slotsClient slots.Slots
	namesClient names.Names
	mu          sync.RWMutex
	reviewSlots map[string]string // token -> slotID
	commitIndex map[string]string // commitHash -> token
	nameIndex   map[string]string // repo:branch or name -> token
}

// NewLocalService creates a new in-process LocalService.
func NewLocalService(store storage.Storage, slotsClient slots.Slots, namesClient names.Names) *LocalService {
	b := make([]byte, 32)
	rand.Read(b)
	id := hex.EncodeToString(b)

	return &LocalService{
		id:          id,
		store:       store,
		slotsClient: slotsClient,
		namesClient: namesClient,
		reviewSlots: make(map[string]string),
		commitIndex: make(map[string]string),
		nameIndex:   make(map[string]string),
	}
}

// ID returns the service ID.
func (s *LocalService) ID() string {
	return s.id
}

func (s *LocalService) saveRecord(ctx context.Context, rec *Record, slotID string, prevAddr string) (string, error) {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", err
	}

	addr, err := s.store.Store(ctx, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("failed to store review in CAS: %w", err)
	}

	if slotID != "" && s.slotsClient != nil {
		err := s.slotsClient.Update(ctx, slotID, addr, prevAddr, nil)
		if err != nil {
			return "", fmt.Errorf("failed to update review slot %s: %w", slotID, err)
		}
	}

	return addr, nil
}

func (s *LocalService) loadRecord(ctx context.Context, slotID string) (*Record, string, error) {
	if s.slotsClient == nil {
		return nil, "", fmt.Errorf("slots client not available")
	}

	addr, err := s.slotsClient.Get(ctx, slotID)
	if err != nil || addr == "" {
		return nil, "", fmt.Errorf("could not read review slot %s: %w", slotID, err)
	}

	rc, ok := s.store.Get(ctx, addr)
	if !ok || rc == nil {
		return nil, "", fmt.Errorf("review content not found in CAS for address %s", addr)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read review CAS data: %w", err)
	}

	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, "", fmt.Errorf("failed to decode review record: %w", err)
	}

	return &rec, addr, nil
}

// RequestReview creates a review record for a change branch and emits a unique review token.
func (s *LocalService) RequestReview(ctx context.Context, repoName, branchName string, author repoid.Identity) (*Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tokenBytes := make([]byte, 8)
	rand.Read(tokenBytes)
	token := fmt.Sprintf("rev-%s", hex.EncodeToString(tokenBytes))

	now := time.Now().Unix()
	rec := &Record{
		Token:      token,
		RepoName:   repoName,
		BranchName: branchName,
		Status:     StatusPending,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	addr, err := s.saveRecord(ctx, rec, "", "")
	if err != nil {
		return nil, err
	}

	var slotID string
	if s.slotsClient != nil {
		slotBytes := make([]byte, 32)
		rand.Read(slotBytes)
		slotID = hex.EncodeToString(slotBytes)

		if err := s.slotsClient.Create(ctx, slotID, addr, ""); err != nil {
			return nil, fmt.Errorf("failed to create review slot %s: %w", slotID, err)
		}
	}

	s.reviewSlots[token] = slotID
	nameKey := fmt.Sprintf("%s:%s", repoName, branchName)
	s.nameIndex[nameKey] = token
	s.nameIndex[branchName] = token

	return rec, nil
}

// GetReview retrieves review metadata and comment threads by token, commit hash, or branch name without mutating review state.
func (s *LocalService) GetReview(ctx context.Context, identifier string) (*Record, error) {
	s.mu.RLock()
	token := identifier
	if t, ok := s.reviewSlots[identifier]; ok {
		token = identifier
		_ = t
	} else if t, ok := s.nameIndex[identifier]; ok {
		token = t
	} else if t, ok := s.commitIndex[identifier]; ok {
		token = t
	}
	slotID := s.reviewSlots[token]
	s.mu.RUnlock()

	if slotID == "" {
		return nil, fmt.Errorf("review %q not found", identifier)
	}

	rec, _, err := s.loadRecord(ctx, slotID)
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// StartReview officially starts a review and transitions its state to StatusInProgress.
func (s *LocalService) StartReview(ctx context.Context, token string, reviewer repoid.Identity) error {
	s.mu.Lock()
	slotID := s.reviewSlots[token]
	if slotID == "" {
		if t, ok := s.nameIndex[token]; ok {
			token = t
			slotID = s.reviewSlots[token]
		}
	}
	s.mu.Unlock()

	if slotID == "" {
		return fmt.Errorf("review %q not found", token)
	}

	rec, prevAddr, err := s.loadRecord(ctx, slotID)
	if err != nil {
		return err
	}

	if rec.Status != StatusPending && rec.Status != StatusInProgress {
		return fmt.Errorf("cannot start review with status %s (review is already closed)", rec.Status)
	}

	rec.Status = StatusInProgress
	if reviewer.Name != "" {
		rec.Reviewer = reviewer.Name
	}
	rec.UpdatedAt = time.Now().Unix()

	_, err = s.saveRecord(ctx, rec, slotID, prevAddr)
	return err
}

// AddComments appends or updates structured comments on a review.
func (s *LocalService) AddComments(ctx context.Context, token string, comments []ReviewComment, author repoid.Identity) error {
	s.mu.Lock()
	slotID := s.reviewSlots[token]
	if slotID == "" {
		if t, ok := s.nameIndex[token]; ok {
			token = t
			slotID = s.reviewSlots[token]
		}
	}
	s.mu.Unlock()

	if slotID == "" {
		return fmt.Errorf("review %q not found", token)
	}

	rec, prevAddr, err := s.loadRecord(ctx, slotID)
	if err != nil {
		return err
	}

	rec.Comments = append(rec.Comments, comments...)
	rec.UpdatedAt = time.Now().Unix()

	_, err = s.saveRecord(ctx, rec, slotID, prevAddr)
	return err
}

// ApproveReview marks the review as approved.
func (s *LocalService) ApproveReview(ctx context.Context, token string, reviewer repoid.Identity) error {
	s.mu.Lock()
	slotID := s.reviewSlots[token]
	if slotID == "" {
		if t, ok := s.nameIndex[token]; ok {
			token = t
			slotID = s.reviewSlots[token]
		}
	}
	s.mu.Unlock()

	if slotID == "" {
		return fmt.Errorf("review %q not found", token)
	}

	rec, prevAddr, err := s.loadRecord(ctx, slotID)
	if err != nil {
		return err
	}

	rec.Status = StatusApproved
	if reviewer.Name != "" {
		rec.Reviewer = reviewer.Name
	}
	rec.UpdatedAt = time.Now().Unix()

	_, err = s.saveRecord(ctx, rec, slotID, prevAddr)
	return err
}

// RejectReview marks the review as rejected.
func (s *LocalService) RejectReview(ctx context.Context, token string, reviewer repoid.Identity) error {
	s.mu.Lock()
	slotID := s.reviewSlots[token]
	if slotID == "" {
		if t, ok := s.nameIndex[token]; ok {
			token = t
			slotID = s.reviewSlots[token]
		}
	}
	s.mu.Unlock()

	if slotID == "" {
		return fmt.Errorf("review %q not found", token)
	}

	rec, prevAddr, err := s.loadRecord(ctx, slotID)
	if err != nil {
		return err
	}

	rec.Status = StatusRejected
	if reviewer.Name != "" {
		rec.Reviewer = reviewer.Name
	}
	rec.UpdatedAt = time.Now().Unix()

	_, err = s.saveRecord(ctx, rec, slotID, prevAddr)
	return err
}

// AbandonReview marks the review as abandoned.
func (s *LocalService) AbandonReview(ctx context.Context, token string, author repoid.Identity) error {
	s.mu.Lock()
	slotID := s.reviewSlots[token]
	if slotID == "" {
		if t, ok := s.nameIndex[token]; ok {
			token = t
			slotID = s.reviewSlots[token]
		}
	}
	s.mu.Unlock()

	if slotID == "" {
		return fmt.Errorf("review %q not found", token)
	}

	rec, prevAddr, err := s.loadRecord(ctx, slotID)
	if err != nil {
		return err
	}

	rec.Status = StatusAbandoned
	rec.UpdatedAt = time.Now().Unix()

	_, err = s.saveRecord(ctx, rec, slotID, prevAddr)
	return err
}

// AssociateCommit maps a commit hash to a review token.
func (s *LocalService) AssociateCommit(token string, commitHash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commitIndex[commitHash] = token
	s.commitIndex[strings.TrimSpace(commitHash)] = token
}
