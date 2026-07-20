package tools

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const goalStateVersion = 1

type durableGoalState struct {
	Version           int    `json:"version"`
	Objective         string `json:"objective"`
	Status            string `json:"status"`
	Message           string `json:"message,omitempty"`
	VerificationRuns  uint32 `json:"verification_runs,omitempty"`
	LastVerification  string `json:"last_verification,omitempty"`
	VerificationStall int    `json:"verification_stall,omitempty"`
	BaselineCommit    string `json:"baseline_commit,omitempty"`
	CreatedAtUnix     int64  `json:"created_at_unix"`
	PlanBaselinePath  string `json:"plan_baseline_path,omitempty"`
	Skeptic0SessionID string `json:"skeptic0_session_id,omitempty"`
}

func (s *GoalStore) saveLocked() error {
	if s.statePath == "" {
		return nil
	}
	data, err := json.Marshal(durableGoalState{
		Version: goalStateVersion, Objective: s.objective, Status: s.status, Message: s.message,
		VerificationRuns: s.verificationRuns, LastVerification: s.lastVerification,
		VerificationStall: s.verificationStall, BaselineCommit: s.baselineCommit,
		CreatedAtUnix: s.createdAtUnix, PlanBaselinePath: s.planBaselinePath,
		Skeptic0SessionID: s.skeptic0SessionID,
	})
	if err != nil {
		return err
	}
	return writeGoalArtifact(s.statePath, data)
}

func (s *GoalStore) loadState() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, statErr := os.Lstat(s.statePath)
	if errors.Is(statErr, os.ErrNotExist) {
		return nil
	}
	if statErr != nil {
		return fmt.Errorf("read goal state: %w", statErr)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return errors.New("goal state must be a regular file no larger than 1 MiB")
	}
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		return fmt.Errorf("read goal state: %w", err)
	}
	var state durableGoalState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("decode goal state: %w", err)
	}
	if state.Version != goalStateVersion {
		return fmt.Errorf("unsupported goal state version %d", state.Version)
	}
	state.Objective = strings.TrimSpace(state.Objective)
	if state.Objective == "" {
		return errors.New("goal state objective is empty")
	}
	if !validGoalStatus(state.Status) {
		state.Status = "paused"
		state.Message = "goal state used an unknown status and was paused"
	}
	if !validGoalObjectID(state.BaselineCommit) {
		state.BaselineCommit = ""
	}
	if !validGoalArtifactPath(s.artifactDir, state.PlanBaselinePath) {
		state.PlanBaselinePath = ""
	}
	if state.CreatedAtUnix <= 0 {
		state.CreatedAtUnix = time.Now().Unix()
	}
	if state.VerificationStall < 0 {
		state.VerificationStall = 0
	}
	if !validGoalSessionID(state.Skeptic0SessionID) {
		state.Skeptic0SessionID = ""
	}
	s.objective, s.status, s.message = state.Objective, state.Status, state.Message
	s.verificationRuns, s.lastVerification = state.VerificationRuns, state.LastVerification
	s.verificationStall, s.baselineCommit = state.VerificationStall, state.BaselineCommit
	s.createdAtUnix, s.planBaselinePath = state.CreatedAtUnix, state.PlanBaselinePath
	s.skeptic0SessionID = state.Skeptic0SessionID
	return nil
}

func (s *GoalStore) Resume() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.objective == "" {
		return "", errors.New("no persisted goal is available")
	}
	if s.status == "completed" {
		return "", errors.New("completed goal cannot be resumed")
	}
	s.status, s.message = "active", ""
	s.verificationRuns, s.verificationStall = 0, 0
	return s.objective, s.saveLocked()
}

func (s *GoalStore) skepticResumeState() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.skeptic0SessionID, s.lastVerification
}

func (s *GoalStore) recordSkeptic0Session(id string, clear bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if clear || id != "" {
		s.skeptic0SessionID = id
		_ = s.saveLocked()
	}
}

func validGoalStatus(status string) bool {
	switch status {
	case "active", "verifying", "paused", "blocked", "completed":
		return true
	default:
		return false
	}
}

func validGoalObjectID(value string) bool {
	if value == "" {
		return true
	}
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validGoalSessionID(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 128 {
		return false
	}
	for index, char := range value {
		alphanumeric := (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9')
		if !alphanumeric && (index == 0 || char != '.' && char != '-' && char != '_') {
			return false
		}
	}
	return true
}

func validGoalArtifactPath(root, path string) bool {
	if path == "" {
		return true
	}
	if !filepath.IsAbs(path) || !pathWithin(root, path) {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}
