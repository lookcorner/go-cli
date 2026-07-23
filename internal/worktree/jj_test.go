package worktree

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestJujutsuOperations(t *testing.T) {
	root := fakeJujutsuRepo(t)
	ctx := context.Background()
	if !IsJujutsu(root) {
		t.Fatal("colocated jj repository was not detected")
	}
	info, err := JJInfo(ctx, root)
	if err != nil || info.Root != root || info.CurrentBranch != "main" || info.VCSKind != "jujutsuColocated" {
		t.Fatalf("info=%#v err=%v", info, err)
	}
	status := JJStatus(ctx, root)
	if status.Root != root || status.Commit != "abcdef123456" || status.Branch != "main" || len(status.Staged) != 0 || len(status.Unstaged) != 4 {
		t.Fatalf("status=%#v", status)
	}
	for index, want := range []struct{ path, kind string }{{"edited.txt", "edit"}, {"new.txt", "create"}, {"old.txt", "delete"}, {"old.txt => renamed.txt", "rename"}} {
		if got := status.Unstaged[index]; got.Path != want.path || got.Type != want.kind || got.Staged {
			t.Fatalf("change %d=%#v", index, got)
		}
	}
	if commit := JJCurrentCommit(ctx, root); commit == nil || *commit != strings.Repeat("a", 40) {
		t.Fatalf("commit=%v", commit)
	}
	branches, err := JJBranches(ctx, root)
	if err != nil || branches.CurrentBranch != "main" || len(branches.Branches) != 2 || !branches.Branches[0].Current || !branches.Branches[1].Remote {
		t.Fatalf("branches=%#v err=%v", branches, err)
	}
	committed, err := JJCommit(ctx, root, "ship it")
	if err != nil || committed.CommitHash != "abcdef123456" || committed.Output != "Commit described and new change started" {
		t.Fatalf("committed=%#v err=%v", committed, err)
	}
	if err := JJDiscard(ctx, root, []string{"one.txt", "two.txt"}); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(filepath.Join(root, "jj.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"describe -m ship it", "new", "restore one.txt two.txt"} {
		if !strings.Contains(string(log), command+"\n") {
			t.Fatalf("missing command %q in %q", command, log)
		}
	}
}

func TestJujutsuCurrentCommitIsLenient(t *testing.T) {
	root := fakeJujutsuRepo(t)
	t.Setenv("JJ_FAIL_COMMIT", "1")
	if commit := JJCurrentCommit(context.Background(), root); commit != nil {
		t.Fatalf("commit=%v", commit)
	}
}

func fakeJujutsuRepo(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-backed jj fixture is Unix-only")
	}
	root := newRepo(t)
	if err := os.Mkdir(filepath.Join(root, ".jj"), 0o700); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$JJ_LOG"
case "$*" in
  "--ignore-working-copy workspace root") printf '%s\n' "$JJ_ROOT" ;;
  "--ignore-working-copy log --no-graph -r @ -T commit_id.shortest(12)") printf 'abcdef123456\n' ;;
  "--ignore-working-copy log --no-graph -r @ -T commit_id")
    [ "$JJ_FAIL_COMMIT" = 1 ] && exit 1
    printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n'
    ;;
  "--ignore-working-copy log --no-graph -r @- -T bookmarks.join(\", \")") printf 'main\n' ;;
  "--ignore-working-copy diff --summary") printf 'M edited.txt\nA new.txt\nD old.txt\nR old.txt => renamed.txt\n' ;;
  "--ignore-working-copy bookmark list --all -T name ++ if(remote, \"@\" ++ remote, \"\") ++ \"\\n\"") printf 'main\nmain@origin\n' ;;
esac
`
	path := filepath.Join(bin, "jj")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("JJ_ROOT", root)
	t.Setenv("JJ_LOG", filepath.Join(root, "jj.log"))
	return root
}
