package tui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

const externalPromptMaxBytes = 4 << 20

type externalEditorDoneEvent struct {
	path     string
	original string
	err      error
}

func (m *model) openExternalPromptEditor() tea.Cmd {
	if !m.minimal {
		m.appendSystem("/edit-prompt is only available in minimal mode")
		m.status = "command unavailable"
		return nil
	}
	if m.externalEditorPath != "" {
		return nil
	}
	if m.inlineEdit != nil || len(m.slashSuggestions()) > 0 {
		return nil
	}
	if len(m.promptImages) > 0 {
		m.appendSystem("External prompt editing is not available while the draft has attachments.")
		m.status = "external editor unavailable"
		return nil
	}
	if m.voiceStarting || m.voiceSession != nil {
		m.appendSystem("External prompt editing is not available while voice input is active.")
		m.status = "external editor unavailable"
		return nil
	}
	argv, err := editorArgv(os.Getenv("VISUAL"), os.Getenv("EDITOR"))
	if err != nil {
		m.externalEditorFailure(err.Error())
		return nil
	}
	original := string(m.input)
	file, err := os.CreateTemp("", "gork-prompt-*.md")
	if err != nil {
		m.externalEditorFailure("Could not open the draft in an external editor; the original draft was kept.")
		return nil
	}
	path := file.Name()
	if _, err = file.WriteString(original); err == nil {
		err = file.Close()
	} else {
		_ = file.Close()
	}
	if err != nil {
		_ = os.Remove(path)
		m.externalEditorFailure("Could not open the draft in an external editor; the original draft was kept.")
		return nil
	}
	m.externalEditorPath = path
	m.status = "editing prompt externally"
	command := exec.Command(argv[0], append(argv[1:], path)...)
	return tea.ExecProcess(command, func(err error) tea.Msg {
		return externalEditorDoneEvent{path: path, original: original, err: err}
	})
}

func (m *model) finishExternalPromptEditor(event externalEditorDoneEvent) {
	if event.path == "" || event.path != m.externalEditorPath {
		return
	}
	defer os.Remove(event.path)
	m.externalEditorPath = ""
	if string(m.input) != event.original {
		m.externalEditorFailure("The draft changed while the external editor was open; the newer draft was kept.")
		return
	}
	if event.err != nil {
		var exitError *exec.ExitError
		if errors.As(event.err, &exitError) {
			m.externalEditorFailure("External prompt editor exited unsuccessfully; the original draft was kept.")
		} else {
			m.externalEditorFailure("External prompt editor failed; the original draft was kept.")
		}
		return
	}
	info, err := os.Stat(event.path)
	if err != nil {
		m.externalEditorFailure("External prompt editor failed; the original draft was kept.")
		return
	}
	if info.Size() > externalPromptMaxBytes {
		m.externalEditorFailure("External prompt editor saved a draft larger than 4 MiB; the original draft was kept.")
		return
	}
	file, err := os.Open(event.path)
	if err != nil {
		m.externalEditorFailure("External prompt editor failed; the original draft was kept.")
		return
	}
	content, err := io.ReadAll(io.LimitReader(file, externalPromptMaxBytes+1))
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		m.externalEditorFailure("External prompt editor failed; the original draft was kept.")
		return
	}
	if len(content) > externalPromptMaxBytes {
		m.externalEditorFailure("External prompt editor saved a draft larger than 4 MiB; the original draft was kept.")
		return
	}
	if !utf8.Valid(content) {
		m.externalEditorFailure("External prompt editor saved invalid UTF-8; the original draft was kept.")
		return
	}
	m.input = []rune(string(content))
	m.cursor = len(m.input)
	m.inputUndo = nil
	m.historyActive = false
	m.historyIndex = -1
	m.slashSelected = 0
	m.slashQuery = ""
	m.slashDismissed = ""
	m.clearPromptSuggestion()
	m.status = "prompt updated"
}

func (m *model) externalEditorFailure(message string) {
	m.appendSystem(message)
	m.status = "external editor failed"
}

func editorArgv(visual, editor string) ([]string, error) {
	command := strings.TrimSpace(visual)
	if command == "" {
		command = strings.TrimSpace(editor)
	}
	if command == "" {
		command = "vi"
	}
	argv, err := splitEditorCommand(command)
	if err != nil || len(argv) == 0 || argv[0] == "" {
		return nil, errors.New("could not parse $VISUAL or $EDITOR")
	}
	return argv, nil
}

// splitEditorCommand implements the quoting needed by VISUAL and EDITOR
// without invoking a shell.
func splitEditorCommand(command string) ([]string, error) {
	var (
		arguments []string
		word      strings.Builder
		quote     rune
		escaped   bool
		started   bool
	)
	flush := func() {
		if started {
			arguments = append(arguments, word.String())
			word.Reset()
			started = false
		}
	}
	for _, r := range command {
		if escaped {
			word.WriteRune(r)
			started, escaped = true, false
			continue
		}
		if quote == '\'' {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			started = true
			continue
		}
		if r == '\\' {
			escaped, started = true, true
			continue
		}
		if quote == '"' {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			started = true
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote, started = r, true
		case unicode.IsSpace(r):
			flush()
		default:
			word.WriteRune(r)
			started = true
		}
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unterminated editor command")
	}
	flush()
	return arguments, nil
}
