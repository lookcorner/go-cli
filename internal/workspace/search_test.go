package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestContentSearchArgs(t *testing.T) {
	args := contentSearchArgs(ContentSearchRequest{
		Pattern: "needle", WholeWord: true, IncludeGlobs: []string{"*.go"}, ExcludeGlobs: []string{"generated/**"}, RespectGitignore: true,
	})
	for _, want := range []string{"--fixed-strings", "--word-regexp", "*.go", "!generated/**", "needle"} {
		if !slices.Contains(args, want) {
			t.Fatalf("arguments %q do not contain %q", args, want)
		}
	}
	if slices.Contains(args, "--no-ignore") {
		t.Fatalf("respectGitignore search disabled ignores: %q", args)
	}
	regexArgs := contentSearchArgs(ContentSearchRequest{Pattern: "need.*", IsRegex: true})
	if slices.Contains(regexArgs, "--fixed-strings") || !slices.Contains(regexArgs, "--no-ignore") {
		t.Fatalf("unexpected regex arguments: %q", regexArgs)
	}
}

func TestSearchContentReturnsBoundedResultsAndFinalBatch(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep is not installed")
	}
	root := t.TempDir()
	for name, content := range map[string]string{
		"first.go": "package fixture\n// Needle here\n", "second.go": "package fixture\n// Needle again\n", "ignored.txt": "Needle\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	one := 1
	var batches []ContentSearchBatch
	result, err := SearchContent(context.Background(), root, ContentSearchRequest{
		Pattern: "Needle", IncludeGlobs: []string{"*.go"}, MaxMatches: &one, RespectGitignore: true,
	}, func(batch ContentSearchBatch) { batches = append(batches, batch) })
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalMatches != 1 || result.TotalFiles != 1 || !result.Truncated || len(result.Files[0].Matches) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	match := result.Files[0].Matches[0]
	if match.Line != 2 || !strings.HasPrefix(match.Content, "// Needle ") || match.MatchStart == nil || *match.MatchStart != 3 {
		t.Fatalf("unexpected match: %#v", match)
	}
	if len(batches) == 0 || !batches[len(batches)-1].Done || !batches[len(batches)-1].Truncated {
		t.Fatalf("missing final batch: %#v", batches)
	}
}

func TestSearchContentNoMatchesStillCompletes(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep is not installed")
	}
	var batches []ContentSearchBatch
	result, err := SearchContent(context.Background(), t.TempDir(), ContentSearchRequest{Pattern: "absent", RespectGitignore: true}, func(batch ContentSearchBatch) {
		batches = append(batches, batch)
	})
	if err != nil || result.TotalMatches != 0 || len(batches) != 1 || !batches[0].Done {
		t.Fatalf("result=%#v batches=%#v err=%v", result, batches, err)
	}
}

func TestSearchContentModesAndIgnoreRules(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep is not installed")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"exact.txt": "Needle\n", "plural.txt": "Needles\n", "ignored.txt": "needle\n", ".gitignore": "ignored.txt\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name string
		req  ContentSearchRequest
		want int
	}{
		{"whole word", ContentSearchRequest{Pattern: "Needle", WholeWord: true, RespectGitignore: true}, 1},
		{"regex", ContentSearchRequest{Pattern: `Needle(s)?`, IsRegex: true, RespectGitignore: true}, 2},
		{"respect ignore", ContentSearchRequest{Pattern: "needle", CaseInsensitive: true, RespectGitignore: true}, 2},
		{"disable ignore", ContentSearchRequest{Pattern: "needle", CaseInsensitive: true}, 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := SearchContent(context.Background(), root, test.req, nil)
			if err != nil || result.TotalMatches != test.want {
				t.Fatalf("matches=%d want=%d err=%v result=%#v", result.TotalMatches, test.want, err, result)
			}
		})
	}
}
