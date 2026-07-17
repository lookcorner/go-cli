package tools

import (
	"context"
	"strings"
	"testing"
)

type recordingApprover struct{ calls int }

func (a *recordingApprover) Approve(context.Context, string, string) error {
	a.calls++
	return nil
}

func TestRuleApproverDenyPrecedesAllowAndFallsBack(t *testing.T) {
	base := &recordingApprover{}
	approver, err := NewRuleApprover(base,
		[]string{"Bash(git *)", "write_file(docs/*)"},
		[]string{"Bash(git push --force*)"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := approver.Approve(context.Background(), "shell", "git status"); err != nil {
		t.Fatalf("allowed command was rejected: %v", err)
	}
	if base.calls != 0 {
		t.Fatal("allow rule should bypass base approver")
	}
	if err := approver.Approve(context.Background(), "run terminal command", "git push --force origin main"); err == nil || !strings.Contains(err.Error(), "denied by rule") {
		t.Fatalf("deny rule did not win: %v", err)
	}
	if err := approver.Approve(context.Background(), "write_file", "docs/readme.md"); err != nil {
		t.Fatal(err)
	}
	if err := approver.Approve(context.Background(), "shell", "go test ./..."); err != nil {
		t.Fatal(err)
	}
	if base.calls != 1 {
		t.Fatalf("unmatched action did not fall back: %d", base.calls)
	}
}

func TestRuleApproverRejectsMalformedRules(t *testing.T) {
	if _, err := NewRuleApprover(&recordingApprover{}, []string{"Bash("}, nil); err == nil {
		t.Fatal("expected malformed rule error")
	}
}
