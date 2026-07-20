package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

func goalScratchPaths(artifactDir, goalID string) (string, string) {
	if artifactDir == "" || goalID == "" {
		return "", ""
	}
	digest := sha256.Sum256([]byte(goalID))
	root := filepath.Join(artifactDir, "goal-scratch-"+hex.EncodeToString(digest[:6]))
	return root, filepath.Join(root, "implementer")
}

func (s *GoalStore) prepareScratchLocked() {
	root, implementer := goalScratchPaths(s.artifactDir, s.goalID)
	s.scratchDir, s.scratchReady = implementer, false
	if root == "" || ensurePrivateArtifactDir(root) != nil || ensurePrivateArtifactDir(implementer) != nil {
		return
	}
	s.scratchReady = true
}

func (s *GoalStore) cleanupScratchLocked() {
	root, _ := goalScratchPaths(s.artifactDir, s.goalID)
	if root != "" && pathWithin(s.artifactDir, root) {
		_ = os.RemoveAll(root)
	}
	s.scratchReady = false
}

func (r *Registry) GoalScratch() (string, bool) {
	if r == nil || r.goal == nil {
		return "", false
	}
	r.goal.mu.Lock()
	defer r.goal.mu.Unlock()
	return r.goal.scratchDir, r.goal.scratchReady
}
