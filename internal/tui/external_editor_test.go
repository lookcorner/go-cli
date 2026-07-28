package tui

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/api"
)

func TestEditorArgvUsesReferencePrecedenceAndQuoting(t *testing.T) {
	tests := []struct {
		name           string
		visual, editor string
		want           []string
		wantError      bool
	}{
		{name: "visual", visual: `code --wait "profile name"`, editor: "nano", want: []string{"code", "--wait", "profile name"}},
		{name: "editor", editor: `'my editor' -f ''`, want: []string{"my editor", "-f", ""}},
		{name: "default", want: []string{"vi"}},
		{name: "escaped space", visual: `editor path\ with\ spaces`, want: []string{"editor", "path with spaces"}},
		{name: "unterminated", visual: `editor "broken`, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := editorArgv(test.visual, test.editor)
			if test.wantError {
				if err == nil {
					t.Fatalf("argv=%#v", got)
				}
				return
			}
			if err != nil || !slices.Equal(got, test.want) {
				t.Fatalf("argv=%#v err=%v want=%#v", got, err, test.want)
			}
		})
	}
}

func TestExternalPromptEditorReplacesDraftWithoutChangingComposerMode(t *testing.T) {
	t.Setenv("VISUAL", "true")
	m := &model{
		minimal: true, input: []rune("draft\n"), cursor: 6, bashInput: true,
		promptSuggestion: "suggestion", suggestionDismissed: true, historyActive: true, historyIndex: 2,
	}
	command := m.openExternalPromptEditor()
	if command == nil || m.externalEditorPath == "" || m.status != "editing prompt externally" {
		t.Fatalf("command=%v path=%q status=%q", command != nil, m.externalEditorPath, m.status)
	}
	path := m.externalEditorPath
	if content, err := os.ReadFile(path); err != nil || string(content) != "draft\n" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	if err := os.WriteFile(path, []byte("edited 🌍\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	updated, followup := m.update(externalEditorDoneEvent{path: path, original: "draft\n"})
	m = updated.(*model)
	if followup != nil || string(m.input) != "edited 🌍\n\n" || m.cursor != len([]rune("edited 🌍\n\n")) ||
		!m.bashInput || m.historyActive || m.historyIndex != -1 || m.status != "prompt updated" || m.externalEditorPath != "" || m.promptSuggestion != "" {
		t.Fatalf("followup=%v input=%q cursor=%d bash=%v status=%q path=%q suggestion=%q", followup != nil, m.input, m.cursor, m.bashInput, m.status, m.externalEditorPath, m.promptSuggestion)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary prompt remains: %v", err)
	}
}

func TestExternalPromptEditorGuardsModesAndKeepsOriginalOnFailure(t *testing.T) {
	t.Setenv("VISUAL", "true")
	full := &model{input: []rune("draft")}
	if command := full.openExternalPromptEditor(); command != nil || full.status != "command unavailable" || !strings.Contains(full.transcript.String(), "only available in minimal") {
		t.Fatalf("command=%v status=%q transcript=%q", command != nil, full.status, full.transcript.String())
	}

	attached := &model{minimal: true, input: []rune("draft"), promptImages: []api.ContentPart{{Type: "input_image"}}}
	if command := attached.openExternalPromptEditor(); command != nil || !strings.Contains(attached.transcript.String(), "attachments") {
		t.Fatalf("command=%v transcript=%q", command != nil, attached.transcript.String())
	}

	m := &model{minimal: true, input: []rune("draft"), cursor: 5}
	if command := m.openExternalPromptEditor(); command == nil {
		t.Fatal("editor did not open")
	}
	path := m.externalEditorPath
	m.finishExternalPromptEditor(externalEditorDoneEvent{path: path, original: "draft", err: errors.New("exit 1")})
	if string(m.input) != "draft" || m.externalEditorPath != "" || !strings.Contains(m.transcript.String(), "original draft was kept") {
		t.Fatalf("input=%q path=%q transcript=%q", m.input, m.externalEditorPath, m.transcript.String())
	}
}

func TestExternalPromptEditorRejectsStaleInvalidAndOversizedResults(t *testing.T) {
	t.Setenv("VISUAL", "true")
	tests := []struct {
		name    string
		prepare func(*testing.T, *model, string)
		want    string
	}{
		{name: "stale", prepare: func(_ *testing.T, m *model, _ string) { m.input = []rune("newer") }, want: "newer draft was kept"},
		{name: "invalid utf8", prepare: func(t *testing.T, _ *model, path string) {
			if err := os.WriteFile(path, []byte{0xff}, 0o600); err != nil {
				t.Fatal(err)
			}
		}, want: "invalid UTF-8"},
		{name: "too large", prepare: func(t *testing.T, _ *model, path string) {
			if err := os.Truncate(path, externalPromptMaxBytes+1); err != nil {
				t.Fatal(err)
			}
		}, want: "larger than 4 MiB"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := &model{minimal: true, input: []rune("draft"), cursor: 5}
			if command := m.openExternalPromptEditor(); command == nil {
				t.Fatal("editor did not open")
			}
			path := m.externalEditorPath
			test.prepare(t, m, path)
			m.finishExternalPromptEditor(externalEditorDoneEvent{path: path, original: "draft"})
			if !strings.Contains(m.transcript.String(), test.want) {
				t.Fatalf("transcript=%q", m.transcript.String())
			}
		})
	}
}

func TestExternalPromptEditorShortcutAndSlashCommandAreMinimalOnly(t *testing.T) {
	t.Setenv("VISUAL", "true")
	full := &model{}
	if full.slashCommandAvailable("edit-prompt") {
		t.Fatal("fullscreen advertised /edit-prompt")
	}
	minimal := &model{minimal: true, input: []rune("preserve"), cursor: 8}
	if !minimal.slashCommandAvailable("edit-prompt") {
		t.Fatal("minimal mode hid /edit-prompt")
	}
	updated, command := minimal.update(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	minimal = updated.(*model)
	if command == nil || minimal.externalEditorPath == "" {
		t.Fatalf("command=%v path=%q", command != nil, minimal.externalEditorPath)
	}
	_ = os.Remove(minimal.externalEditorPath)

	typed := &model{minimal: true}
	typed.setInput("/edit-prompt")
	updated, command = typed.update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	typed = updated.(*model)
	if command == nil || typed.externalEditorPath == "" || len(typed.input) != 0 {
		t.Fatalf("command=%v path=%q input=%q", command != nil, typed.externalEditorPath, typed.input)
	}
	_ = os.Remove(typed.externalEditorPath)
}
