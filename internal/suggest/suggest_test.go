package suggest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHistoryRankingGhostLimitAndUnicodeCursor(t *testing.T) {
	response := Generate(context.Background(), Request{Text: "你好git tail", Cursor: 9, CWD: t.TempDir(), Limit: 1, Generation: 7}, []string{
		"你好git status", "你好git status", "你好git stash",
	}, nil)
	if response.Generation != 7 || len(response.Completions) != 1 {
		t.Fatalf("response=%#v", response)
	}
	row := response.Completions[0]
	if row.InsertText != "你好git status" || row.Priority != 10 || row.ReplaceRange[1] != len("你好git tail") {
		t.Fatalf("row=%#v", row)
	}
	if response.Ghost == nil || response.Ghost.Suffix != " status" || response.Ghost.Source != "history" {
		t.Fatalf("ghost=%#v", response.Ghost)
	}

	// Byte 1 is inside the first rune and must clamp back to byte zero.
	clamped := Generate(context.Background(), Request{Text: "你x", Cursor: 1, CWD: t.TempDir(), Limit: 10}, []string{"你x extra"}, nil)
	if clamped.Ghost != nil {
		t.Fatalf("ghost=%#v", clamped.Ghost)
	}
}

func TestTokenOnlyPATHAndCommandPosition(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "gork-safe"))
	t.Setenv("PATH", bin)
	aiCalled := false
	response := Generate(context.Background(), Request{Text: "gork", Cursor: 4, CWD: t.TempDir(), Limit: 10, IncludeAI: true, TokenOnly: true}, []string{"gork history"}, func(context.Context, string, string, string) (string, error) {
		aiCalled = true
		return "gork ai", nil
	})
	if aiCalled || len(response.Completions) != 1 || response.Completions[0].Source != "path" {
		t.Fatalf("response=%#v aiCalled=%v", response, aiCalled)
	}
	row := response.Completions[0]
	if row.InsertText != "gork-safe" || row.TokenText != "gork-safe" || row.ReplaceRange[0] != 0 || row.ReplaceRange[1] != 4 {
		t.Fatalf("row=%#v", row)
	}
	quoted := Generate(context.Background(), Request{Text: `echo "a | gor`, Cursor: len(`echo "a | gor`), CWD: t.TempDir(), Limit: 10, TokenOnly: true}, nil, nil)
	for _, row := range quoted.Completions {
		if row.Source == "path" {
			t.Fatalf("quoted separator offered executable: %#v", quoted)
		}
	}
}

func TestFileSuggestionsQuoteRankAndHide(t *testing.T) {
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, "My Docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "My File.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".secret"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	response := Generate(context.Background(), Request{Text: "cat My", Cursor: 6, CWD: cwd, Limit: 10, TokenOnly: true}, nil, nil)
	if len(response.Completions) != 2 || response.Completions[0].Display != "My Docs/" {
		t.Fatalf("response=%#v", response)
	}
	if response.Completions[0].InsertText != `cat My\ Docs/` || response.Completions[0].Priority != 2 {
		t.Fatalf("directory=%#v", response.Completions[0])
	}
	if response.Completions[1].TokenText != `My\ File.txt` || response.Completions[1].InsertText != `cat My\ File.txt` {
		t.Fatalf("file=%#v", response.Completions[1])
	}
	for _, row := range Generate(context.Background(), Request{Text: "cat ", Cursor: 4, CWD: cwd, Limit: 10, TokenOnly: true}, nil, nil).Completions {
		if row.Display == ".secret" {
			t.Fatal("hidden file returned without dot prefix")
		}
	}
	hidden := Generate(context.Background(), Request{Text: "cat .s", Cursor: 6, CWD: cwd, Limit: 10, TokenOnly: true}, nil, nil)
	if len(hidden.Completions) != 1 || hidden.Completions[0].Display != ".secret" {
		t.Fatalf("hidden=%#v", hidden)
	}
	flags := Generate(context.Background(), Request{Text: "cat --he", Cursor: 8, CWD: cwd, Limit: 10, TokenOnly: true}, nil, nil)
	if len(flags.Completions) != 0 {
		t.Fatalf("flags=%#v", flags)
	}
}

func TestRedirectAndCompletionTruncation(t *testing.T) {
	cwd := t.TempDir()
	for index := range 51 {
		name := filepath.Join(cwd, "log"+string(rune('a'+index%26))+string(rune('A'+index/26)))
		if err := os.WriteFile(name, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	response := Generate(context.Background(), Request{Text: "echo > lo", Cursor: 9, CWD: cwd, Limit: 100, TokenOnly: true}, nil, nil)
	if len(response.Completions) != 50 {
		t.Fatalf("count=%d", len(response.Completions))
	}
	for _, row := range response.Completions {
		if row.Source != "file" || !row.Truncated || row.ReplaceRange[0] != 7 || row.InsertText[:7] != "echo > " {
			t.Fatalf("row=%#v", row)
		}
	}
}

func TestAISuggestionAndHistorySkip(t *testing.T) {
	called := 0
	ai := func(_ context.Context, prefix, cwd, model string) (string, error) {
		called++
		if prefix != "git" || model != "fast" {
			t.Fatalf("prefix=%q model=%q", prefix, model)
		}
		return " commit --amend", nil
	}
	response := Generate(context.Background(), Request{Text: "git", Cursor: 3, CWD: "/work", Limit: 10, IncludeAI: true, AIModel: "fast"}, nil, ai)
	if called != 1 || response.Ghost == nil || response.Ghost.FullText != "git commit --amend" || response.Ghost.Source != "ai" {
		t.Fatalf("response=%#v called=%d", response, called)
	}
	Generate(context.Background(), Request{Text: "git", Cursor: 3, CWD: "/work", Limit: 10, IncludeAI: true}, []string{"git add", "git branch", "git commit"}, ai)
	if called != 1 {
		t.Fatal("AI was not skipped for strong history")
	}
	failed := Generate(context.Background(), Request{Text: "do", Cursor: 2, CWD: "/work", Limit: 10, IncludeAI: true}, nil, func(context.Context, string, string, string) (string, error) { return "", errors.New("offline") })
	if failed.Ghost != nil {
		t.Fatalf("failed=%#v", failed)
	}
}

func TestDirectoryExpansionRespectsQuoteProvenance(t *testing.T) {
	t.Setenv("SUGGEST_ROOT", "/expanded")
	if got := expandDir("$SUGGEST_ROOT/", []bool{true, true, true, true, true, true, true, true, true, true, true, true, true, true}); got != "/expanded/" {
		t.Fatalf("plain expansion=%q", got)
	}
	if got := expandDir("$SUGGEST_ROOT/", make([]bool, len("$SUGGEST_ROOT/"))); got != "$SUGGEST_ROOT/" {
		t.Fatalf("quoted expansion=%q", got)
	}
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}
