package terminaldiag

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const managedNamespace = "gork doctor"

type managedItem struct {
	ID   string
	Body string
}

func managedOuterOpen() string  { return "# >>> " + managedNamespace + " >>>" }
func managedOuterClose() string { return "# <<< " + managedNamespace + " <<<" }
func managedItemOpen(id string) string {
	return "# >>> " + id + " >>>"
}
func managedItemClose(id string) string {
	return "# <<< " + id + " <<<"
}

func managedBlock(items []managedItem) string {
	parts := []string{managedOuterOpen()}
	for _, item := range items {
		parts = append(parts, managedItemOpen(item.ID), item.Body, managedItemClose(item.ID))
	}
	parts = append(parts, managedOuterClose())
	return strings.Join(parts, "\n")
}

func upsertManagedItem(path, id, body string) (written bool, err error) {
	original, err := readFileOrEmpty(path)
	if err != nil {
		return false, err
	}
	unmanaged, items := splitManagedAll(original)
	updatedItems := make([]managedItem, 0, len(items)+1)
	found := false
	unchanged := false
	for _, item := range items {
		if item.ID != id {
			updatedItems = append(updatedItems, item)
			continue
		}
		found = true
		if item.Body == body {
			unchanged = true
			updatedItems = append(updatedItems, item)
			continue
		}
		updatedItems = append(updatedItems, managedItem{ID: id, Body: body})
	}
	if !found {
		updatedItems = append(updatedItems, managedItem{ID: id, Body: body})
	}
	if found && unchanged {
		return false, nil
	}
	updated := strings.TrimRight(unmanaged, "\n")
	if updated != "" {
		updated += "\n"
	}
	updated += managedBlock(updatedItems) + "\n"
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
	_, items := splitManagedAll(data)
	for _, item := range items {
		if item.ID == id {
			return item.Body == body
		}
	}
	return false
}

func unmanagedText(path string) (string, error) {
	data, err := readFileOrEmpty(path)
	if err != nil {
		return "", err
	}
	unmanaged, _ := splitManagedAll(data)
	return unmanaged, nil
}

func splitManagedAll(text string) (unmanaged string, items []managedItem) {
	open := managedOuterOpen()
	closeMarker := managedOuterClose()
	start := strings.Index(text, open)
	if start < 0 {
		return text, nil
	}
	end := strings.Index(text[start:], closeMarker)
	if end < 0 {
		return text, nil
	}
	end = start + end + len(closeMarker)
	for end < len(text) && (text[end] == '\n' || text[end] == '\r') {
		end++
	}
	block := text[start:end]
	unmanaged = text[:start] + text[end:]
	rest := block
	for {
		itemStart := strings.Index(rest, "# >>> terminal.")
		if itemStart < 0 {
			break
		}
		lineEnd := strings.IndexByte(rest[itemStart:], '\n')
		if lineEnd < 0 {
			break
		}
		openLine := strings.TrimSpace(rest[itemStart : itemStart+lineEnd])
		id, ok := strings.CutPrefix(openLine, "# >>> ")
		if !ok {
			rest = rest[itemStart+lineEnd+1:]
			continue
		}
		id, ok = strings.CutSuffix(id, " >>>")
		if !ok || !strings.HasPrefix(id, "terminal.") {
			rest = rest[itemStart+lineEnd+1:]
			continue
		}
		closeLine := managedItemClose(id)
		bodyStart := itemStart + lineEnd + 1
		itemEnd := strings.Index(rest[bodyStart:], closeLine)
		if itemEnd < 0 {
			break
		}
		itemEnd = bodyStart + itemEnd
		body := strings.Trim(rest[bodyStart:itemEnd], "\r\n")
		items = append(items, managedItem{ID: id, Body: body})
		rest = rest[itemEnd+len(closeLine):]
	}
	return unmanaged, items
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
