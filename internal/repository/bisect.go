package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"invariant/internal/content"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// BisectState tracks the active binary search regression state.
type BisectState struct {
	OriginalHead     string   `json:"originalHead"`
	CurrentCandidate string   `json:"currentCandidate"`
	GoodCommits      []string `json:"goodCommits"`
	BadCommits       []string `json:"badCommits"`
}

const bisectFileName = ".invariant-bisect.json"

func readBisectState(wsRoot string) (*BisectState, error) {
	bisectPath := filepath.Join(wsRoot, bisectFileName)
	data, err := os.ReadFile(bisectPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state BisectState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func writeBisectState(wsRoot string, state *BisectState) error {
	bisectPath := filepath.Join(wsRoot, bisectFileName)
	if state == nil {
		_ = os.Remove(bisectPath)
		return nil
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(bisectPath, data, 0644)
}

// BisectStart begins a bisect session.
func BisectStart(ctx context.Context, store storage.Storage, slotsClient slots.Slots, commitSvc commit.Service, workspaceDir, badCommit, goodCommit string) (string, int, error) {
	wsRoot, meta, err := FindWorkspaceRoot(workspaceDir)
	if err != nil {
		return "", 0, err
	}

	headHash := meta.CommitHash
	if meta.SlotID != "" {
		slotAddr, err := slotsClient.Get(ctx, meta.SlotID)
		if err == nil && slotAddr != "" {
			headHash = slotAddr
		}
	}

	state := &BisectState{
		OriginalHead: headHash,
	}

	if badCommit != "" {
		state.BadCommits = append(state.BadCommits, badCommit)
	} else {
		state.BadCommits = append(state.BadCommits, headHash)
	}

	if goodCommit != "" {
		state.GoodCommits = append(state.GoodCommits, goodCommit)
	}

	if len(state.GoodCommits) > 0 && len(state.BadCommits) > 0 {
		candidate, remaining, err := commitSvc.Bisect(ctx, state.GoodCommits, state.BadCommits)
		if err != nil {
			return "", 0, err
		}
		state.CurrentCandidate = candidate
		if err := writeBisectState(wsRoot, state); err != nil {
			return "", 0, err
		}

		if candidate != "" {
			c, err := commitSvc.GetCommit(ctx, candidate)
			if err == nil && c != nil {
				_ = MaterializeTree(ctx, content.ContentLink{Address: c.Tree.Address}, wsRoot, store)
			}
		}

		return candidate, remaining, nil
	}

	if err := writeBisectState(wsRoot, state); err != nil {
		return "", 0, err
	}

	return "", 0, nil
}

// BisectMark marks the current candidate or specified commit as good or bad.
func BisectMark(ctx context.Context, store storage.Storage, slotsClient slots.Slots, commitSvc commit.Service, workspaceDir string, isGood bool, commitHash string) (string, int, bool, error) {
	wsRoot, _, err := FindWorkspaceRoot(workspaceDir)
	if err != nil {
		return "", 0, false, err
	}

	state, err := readBisectState(wsRoot)
	if err != nil || state == nil {
		return "", 0, false, fmt.Errorf("no bisect session in progress (run 'bisect start')")
	}

	target := commitHash
	if target == "" {
		target = state.CurrentCandidate
	}
	if target == "" {
		target = state.OriginalHead
	}

	if isGood {
		state.GoodCommits = append(state.GoodCommits, target)
	} else {
		state.BadCommits = append(state.BadCommits, target)
	}

	if len(state.GoodCommits) == 0 {
		_ = writeBisectState(wsRoot, state)
		return "", 0, false, fmt.Errorf("bisect needs at least one good commit (run 'bisect good <commit>')")
	}
	if len(state.BadCommits) == 0 {
		_ = writeBisectState(wsRoot, state)
		return "", 0, false, fmt.Errorf("bisect needs at least one bad commit (run 'bisect bad <commit>')")
	}

	candidate, remaining, err := commitSvc.Bisect(ctx, state.GoodCommits, state.BadCommits)
	if err != nil {
		return "", 0, false, err
	}

	if remaining == 0 {
		culprit := candidate
		if culprit == "" && len(state.BadCommits) > 0 {
			culprit = state.BadCommits[len(state.BadCommits)-1]
		}
		return culprit, 0, true, nil
	}

	state.CurrentCandidate = candidate
	if err := writeBisectState(wsRoot, state); err != nil {
		return "", 0, false, err
	}

	// Materialize candidate
	c, err := commitSvc.GetCommit(ctx, candidate)
	if err == nil && c != nil {
		_ = MaterializeTree(ctx, content.ContentLink{Address: c.Tree.Address}, wsRoot, store)
	}

	return candidate, remaining, false, nil
}

// BisectReset restores the workspace to the pre-bisect HEAD state.
func BisectReset(ctx context.Context, store storage.Storage, slotsClient slots.Slots, commitSvc commit.Service, workspaceDir string) error {
	wsRoot, _, err := FindWorkspaceRoot(workspaceDir)
	if err != nil {
		return err
	}

	state, err := readBisectState(wsRoot)
	if err != nil || state == nil {
		return nil
	}

	if state.OriginalHead != "" {
		c, err := commitSvc.GetCommit(ctx, state.OriginalHead)
		if err == nil && c != nil {
			_ = MaterializeTree(ctx, content.ContentLink{Address: c.Tree.Address}, wsRoot, store)
		}
	}

	return writeBisectState(wsRoot, nil)
}

// BisectRun executes a test script automatically across bisect candidates until the regression is isolated.
func BisectRun(ctx context.Context, store storage.Storage, slotsClient slots.Slots, commitSvc commit.Service, workspaceDir string, scriptArgs []string) (string, error) {
	if len(scriptArgs) == 0 {
		return "", fmt.Errorf("bisect run requires a command/script to execute")
	}

	for {
		cmd := exec.Command(scriptArgs[0], scriptArgs[1:]...)
		cmd.Dir = workspaceDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		err := cmd.Run()
		isGood := err == nil

		candidate, _, found, bErr := BisectMark(ctx, store, slotsClient, commitSvc, workspaceDir, isGood, "")
		if bErr != nil {
			return "", bErr
		}
		if found {
			return candidate, nil
		}
	}
}
