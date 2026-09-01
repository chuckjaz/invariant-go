package review

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"

	"invariant/internal/identity"
	repoid "invariant/internal/repository/identity"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

type mockReviewService struct {
	mu      sync.Mutex
	reviews map[string]*Record
}

func newMockReviewService() *mockReviewService {
	return &mockReviewService{
		reviews: make(map[string]*Record),
	}
}

func (m *mockReviewService) RequestReview(ctx context.Context, repoName, branchName string, author repoid.Identity) (*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	token := "rev-12345"
	rec := &Record{
		Token:      token,
		RepoName:   repoName,
		BranchName: branchName,
		Status:     StatusPending,
	}
	m.reviews[token] = rec
	return rec, nil
}

func (m *mockReviewService) GetReview(ctx context.Context, identifier string) (*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	rec, ok := m.reviews[identifier]
	if !ok {
		return nil, fmt.Errorf("review %s not found", identifier)
	}
	return rec, nil
}

func (m *mockReviewService) StartReview(ctx context.Context, token string, reviewer repoid.Identity) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	rec, ok := m.reviews[token]
	if !ok {
		return fmt.Errorf("review %s not found", token)
	}
	rec.Status = StatusInProgress
	rec.Reviewer = reviewer.Name
	return nil
}

func (m *mockReviewService) AddComments(ctx context.Context, token string, comments []ReviewComment, author repoid.Identity) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	rec, ok := m.reviews[token]
	if !ok {
		return fmt.Errorf("review %s not found", token)
	}
	rec.Comments = append(rec.Comments, comments...)
	return nil
}

func (m *mockReviewService) ApproveReview(ctx context.Context, token string, reviewer repoid.Identity) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	rec, ok := m.reviews[token]
	if !ok {
		return fmt.Errorf("review %s not found", token)
	}
	rec.Status = StatusApproved
	return nil
}

func (m *mockReviewService) RejectReview(ctx context.Context, token string, reviewer repoid.Identity) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	rec, ok := m.reviews[token]
	if !ok {
		return fmt.Errorf("review %s not found", token)
	}
	rec.Status = StatusRejected
	return nil
}

func (m *mockReviewService) AbandonReview(ctx context.Context, token string, author repoid.Identity) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	rec, ok := m.reviews[token]
	if !ok {
		return fmt.Errorf("review %s not found", token)
	}
	rec.Status = StatusAbandoned
	return nil
}

func TestReviewServerAndClient(t *testing.T) {
	ctx := context.Background()
	mockSvc := newMockReviewService()

	server := NewServer(mockSvc)
	ts := httptest.NewServer(server)
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())

	// Test ID protocol
	if client.ID() == "" || client.ID() != server.ID() {
		t.Errorf("ID protocol mismatch: client.ID()=%q, server.ID()=%q", client.ID(), server.ID())
	}

	// Verify server implements identity.Identity
	var _ identity.Identity = server
	var _ identity.Identity = client

	// 1. Request review
	author := repoid.Identity{Name: "Alice", Email: "alice@example.com"}
	rec, err := client.RequestReview(ctx, "my-repo", "feat-branch", author)
	if err != nil {
		t.Fatalf("client.RequestReview failed: %v", err)
	}
	if rec.Status != StatusPending || rec.Token != "rev-12345" {
		t.Errorf("Unexpected record: %+v", rec)
	}

	// 2. Get review (open for inspection without state change)
	gotRec, err := client.GetReview(ctx, rec.Token)
	if err != nil {
		t.Fatalf("client.GetReview failed: %v", err)
	}
	if gotRec.Status != StatusPending {
		t.Errorf("Expected StatusPending upon viewing review, got %s", gotRec.Status)
	}

	// 3. Start review (transitions to in_progress)
	reviewer := repoid.Identity{Name: "Bob", Email: "bob@example.com"}
	if err := client.StartReview(ctx, rec.Token, reviewer); err != nil {
		t.Fatalf("client.StartReview failed: %v", err)
	}
	gotStarted, _ := client.GetReview(ctx, rec.Token)
	if gotStarted.Status != StatusInProgress || gotStarted.Reviewer != "Bob" {
		t.Errorf("Expected StatusInProgress with reviewer Bob, got: %+v", gotStarted)
	}

	// 4. Add comments
	comments := []ReviewComment{
		{
			File: "main.go",
			Comments: []Comment{
				{Comment: "Looks good, check error handling.", Author: "Bob"},
			},
		},
	}
	if err := client.AddComments(ctx, rec.Token, comments, reviewer); err != nil {
		t.Fatalf("client.AddComments failed: %v", err)
	}
	gotWithComments, _ := client.GetReview(ctx, rec.Token)
	if len(gotWithComments.Comments) != 1 {
		t.Errorf("Expected 1 comment thread, got %d", len(gotWithComments.Comments))
	}

	// 5. Approve review
	if err := client.ApproveReview(ctx, rec.Token, reviewer); err != nil {
		t.Fatalf("client.ApproveReview failed: %v", err)
	}
	gotApproved, _ := client.GetReview(ctx, rec.Token)
	if gotApproved.Status != StatusApproved {
		t.Errorf("Expected StatusApproved, got %s", gotApproved.Status)
	}

	// 6. View approved/closed review without state change
	gotClosed, err := client.GetReview(ctx, rec.Token)
	if err != nil {
		t.Fatalf("Failed to retrieve closed review: %v", err)
	}
	if gotClosed.Status != StatusApproved {
		t.Errorf("Expected StatusApproved for closed review, got %s", gotClosed.Status)
	}
}

func TestLocalService(t *testing.T) {
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotsClient := slots.NewMemorySlots("slots-id")

	localSvc := NewLocalService(store, slotsClient, nil)

	// ID protocol
	if localSvc.ID() == "" {
		t.Errorf("Expected non-empty ID for LocalService")
	}

	// 1. RequestReview
	author := repoid.Identity{Name: "Alice", Email: "alice@example.com"}
	rec, err := localSvc.RequestReview(ctx, "my-repo", "feat-branch", author)
	if err != nil {
		t.Fatalf("RequestReview failed: %v", err)
	}
	if rec.Status != StatusPending || rec.Token == "" {
		t.Errorf("Unexpected record: %+v", rec)
	}

	// 2. GetReview (pending)
	got, err := localSvc.GetReview(ctx, rec.Token)
	if err != nil {
		t.Fatalf("GetReview failed: %v", err)
	}
	if got.Status != StatusPending {
		t.Errorf("Expected StatusPending, got %s", got.Status)
	}

	// Also test GetReview by branch name
	gotByBranch, err := localSvc.GetReview(ctx, "feat-branch")
	if err != nil || gotByBranch.Token != rec.Token {
		t.Errorf("GetReview by branch name failed: got=%+v, err=%v", gotByBranch, err)
	}

	// 3. StartReview
	reviewer := repoid.Identity{Name: "Bob", Email: "bob@example.com"}
	if err := localSvc.StartReview(ctx, rec.Token, reviewer); err != nil {
		t.Fatalf("StartReview failed: %v", err)
	}
	startedRec, _ := localSvc.GetReview(ctx, rec.Token)
	if startedRec.Status != StatusInProgress || startedRec.Reviewer != "Bob" {
		t.Errorf("Expected StatusInProgress with reviewer Bob, got: %+v", startedRec)
	}

	// 4. AddComments
	comments := []ReviewComment{
		{
			File: "cmd/main.go",
			Comments: []Comment{
				{Comment: "Please add comments.", Author: "Bob"},
			},
		},
	}
	if err := localSvc.AddComments(ctx, rec.Token, comments, reviewer); err != nil {
		t.Fatalf("AddComments failed: %v", err)
	}
	withComments, _ := localSvc.GetReview(ctx, rec.Token)
	if len(withComments.Comments) != 1 {
		t.Errorf("Expected 1 comment thread, got %d", len(withComments.Comments))
	}

	// 5. ApproveReview
	if err := localSvc.ApproveReview(ctx, rec.Token, reviewer); err != nil {
		t.Fatalf("ApproveReview failed: %v", err)
	}
	approvedRec, _ := localSvc.GetReview(ctx, rec.Token)
	if approvedRec.Status != StatusApproved {
		t.Errorf("Expected StatusApproved, got %s", approvedRec.Status)
	}

	// 6. Verify cannot start closed review
	if err := localSvc.StartReview(ctx, rec.Token, reviewer); err == nil {
		t.Errorf("Expected error when starting already approved review")
	}

	// 7. Verify can open/view closed review
	closedRec, err := localSvc.GetReview(ctx, rec.Token)
	if err != nil || closedRec.Status != StatusApproved {
		t.Errorf("Failed to open closed review: %+v, err=%v", closedRec, err)
	}
}
