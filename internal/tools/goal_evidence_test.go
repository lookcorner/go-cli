package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/workspace"
)

func runGoalEvidenceGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Gork Test", "GIT_AUTHOR_EMAIL=gork@example.invalid",
		"GIT_COMMITTER_NAME=Gork Test", "GIT_COMMITTER_EMAIL=gork@example.invalid",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestGoalVerificationCapturesGitEvidenceAndDetails(t *testing.T) {
	root := t.TempDir()
	runGoalEvidenceGit(t, root, "init")
	tracked := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGoalEvidenceGit(t, root, "add", "tracked.txt")
	runGoalEvidenceGit(t, root, "commit", "-m", "baseline")

	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	artifactDir := filepath.Join(t.TempDir(), "session-artifacts")
	if err := registry.ConfigureGoalVerification(artifactDir); err != nil {
		t.Fatal(err)
	}
	backend := &goalVerifierBackend{outputs: []string{
		`{"verdict":"not_refuted"}`, `{"verdict":"not_refuted"}`, `{"verdict":"not_refuted"}`,
	}}
	registry.subagents.set(backend)
	if err := registry.BeginGoal("finish the feature"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tracked, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGoalEvidenceGit(t, root, "add", "tracked.txt")
	runGoalEvidenceGit(t, root, "commit", "-m", "implementation")
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&updateGoalTool{store: registry.goal}).Execute(context.Background(), json.RawMessage(`{"completed":true,"message":"done"}`)); err != nil {
		t.Fatal(err)
	}
	if err := registry.StartGoalVerification(10); err != nil {
		t.Fatal(err)
	}
	verification := registry.VerifyGoal(context.Background(), registry.GoalSnapshot(), 3)
	if !verification.Achieved || verification.DetailsPath == "" {
		t.Fatalf("verification=%#v", verification)
	}
	patchPath := filepath.Join(artifactDir, "goal-classifier-1.patch")
	patch, err := os.ReadFile(patchPath)
	if err != nil || !strings.Contains(string(patch), "+after") {
		t.Fatalf("patch=%q err=%v", patch, err)
	}
	for _, request := range backend.requests {
		if !strings.Contains(request.Prompt, "CHANGES_FILE: "+patchPath) || !strings.Contains(request.Prompt, "- tracked.txt") || !strings.Contains(request.Prompt, "- new.txt") {
			t.Fatalf("prompt=%s", request.Prompt)
		}
	}
	details, err := os.ReadFile(verification.DetailsPath)
	if err != nil || !strings.Contains(string(details), "Skeptic 1: not_refuted") {
		t.Fatalf("details=%q err=%v", details, err)
	}
	for _, path := range []string{patchPath, verification.DetailsPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("artifact %s: %v", path, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("artifact %s mode=%v", path, info.Mode().Perm())
		}
	}
	if _, err := registry.Execute(context.Background(), "read_file", json.RawMessage(`{"target_file":"`+patchPath+`"}`)); err != nil {
		t.Fatalf("read evidence artifact: %v", err)
	}
}
