package tips

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMergeOrderAndExclusion(t *testing.T) {
	requirements := Source{Items: []string{"requirements"}}
	user := Source{Items: []string{"user"}}
	managed := Source{Items: []string{"managed"}}
	if got, want := Merge(requirements, user, managed, []string{"remote"}), []string{"requirements", "remote", "user", "managed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tips=%v want=%v", got, want)
	}
	user.ExcludeDefault = true
	if got, want := Merge(requirements, user, managed, []string{"remote"}), []string{"requirements", "user", "managed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("excluded tips=%v want=%v", got, want)
	}
}

func TestPickAndAdvancePersistsAndRecovers(t *testing.T) {
	home := t.TempDir()
	items := []string{"first", "second", "third"}
	for index, want := range []string{"first", "second", "third", "first"} {
		if got := PickAndAdvance(items, home); got != want {
			t.Fatalf("pick %d=%q want=%q", index, got, want)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "tip_cursor.json"), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := PickAndAdvance(items, home); got != "first" {
		t.Fatalf("corrupt cursor pick=%q", got)
	}
	if got := PickAndAdvance(nil, home); got != "" {
		t.Fatalf("empty pick=%q", got)
	}
}
