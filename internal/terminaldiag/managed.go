package terminaldiag

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	managedNamespace = "gork doctor"
	sshWrapItemID    = "terminal.ssh-wrap"
)

func managedOuterOpen() string  { return "# >>> " + managedNamespace + " >>>" }
func managedOuterClose() string { return "# <<< " + managedNamespace + " <<<" }
func managedItemOpen(id string) string {
	return "# >>> " + id + " >>>"
}
func managedItemClose(id string) string {
	return "# <<< " + id + " <<<"
}

func managedBlock(id, body string) string {
	return strings.Join([]string{
		managedOuterOpen(),
		managedItemOpen(id),
		body,
		managedItemClose(id),
		managedOuterClose(),
	}, "\n")
}

func upsertManagedItem(path, id, body string) (written bool, err error) {
	original, err := readFileOrEmpty(path)
	if err != nil {
		return false, err
	}
	unmanaged, existing, exact := splitManaged(original, id)
	if exact && existing == body {
		return false, nil
	}
	block := managedBlock(id, body)
	updated := strings.TrimRight(unmanaged, "\n")
	if updated != "" {
		updated += "\n"
	}
	updated += block + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		return false, err
	}
	return true, nil
}

func managedItemExact(path, id, body string) bool {
	data, err := readFileOrEmpty(path)
	if err != nil {
		return false
	}
	_, existing, exact := splitManaged(data, id)
	return exact && existing == body
}

func unmanagedText(path string) (string, error) {
	data, err := readFileOrEmpty(path)
	if err != nil {
		return "", err
	}
	unmanaged, _, _ := splitManaged(data, sshWrapItemID)
	return unmanaged, nil
}

func splitManaged(text, id string) (unmanaged, body string, exact bool) {
	open := managedOuterOpen()
	closeMarker := managedOuterClose()
	start := strings.Index(text, open)
	if start < 0 {
		return text, "", false
	}
	end := strings.Index(text[start:], closeMarker)
	if end < 0 {
		return text, "", false
	}
	end = start + end + len(closeMarker)
	for end < len(text) && (text[end] == '\n' || text[end] == '\r') {
		end++
	}
	block := text[start:end]
	unmanaged = text[:start] + text[end:]
	itemOpen := managedItemOpen(id)
	itemClose := managedItemClose(id)
	itemStart := strings.Index(block, itemOpen)
	itemEnd := strings.Index(block, itemClose)
	if itemStart < 0 || itemEnd < 0 || itemEnd < itemStart {
		return unmanaged, "", false
	}
	bodyStart := itemStart + len(itemOpen)
	raw := strings.Trim(block[bodyStart:itemEnd], "\r\n")
	return unmanaged, raw, true
}

func readFileOrEmpty(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

func ensureSafeHome(home string) error {
	clean := filepath.Clean(home)
	if !filepath.IsAbs(clean) {
		return fmt.Errorf("HOME must be an absolute directory")
	}
	if clean == string(filepath.Separator) || clean == filepath.VolumeName(clean)+string(filepath.Separator) {
		return fmt.Errorf("refusing unsafe HOME %q", home)
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == "." || part == ".." {
			return fmt.Errorf("refusing unsafe HOME %q", home)
		}
	}
	if strings.ContainsAny(clean, "~\x00") {
		return fmt.Errorf("refusing unsafe HOME %q", home)
	}
	return nil
}
