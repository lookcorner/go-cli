package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"unicode/utf8"
)

const maxTextEditBytes = 8 << 20

type TextPosition struct {
	Line      int
	Character int
}

type TextEdit struct {
	Start   TextPosition
	End     TextPosition
	NewText string
}

// ApplyTextEdits applies non-overlapping UTF-16 ranges to one existing file.
func (w *Workspace) ApplyTextEdits(path string, edits []TextEdit) (string, error) {
	resolved, err := w.Resolve(path)
	if err != nil {
		return "", err
	}
	file, err := os.Open(resolved)
	if err != nil {
		return "", fmt.Errorf("open workspace edit target: %w", err)
	}
	defer file.Close()
	opened, statErr := file.Stat()
	linked, linkErr := os.Lstat(resolved)
	if statErr != nil || linkErr != nil || linked.Mode()&os.ModeSymlink != 0 || !opened.Mode().IsRegular() || !os.SameFile(opened, linked) {
		return "", errors.New("workspace edit target must be an unchanged regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxTextEditBytes+1))
	if err != nil {
		return "", fmt.Errorf("read workspace edit target: %w", err)
	}
	if len(data) > maxTextEditBytes {
		return "", errors.New("workspace edit target exceeds 8 MiB")
	}
	if !utf8.Valid(data) {
		return "", errors.New("workspace edit target is not valid UTF-8")
	}
	type span struct {
		start int
		end   int
		text  string
		order int
	}
	spans := make([]span, len(edits))
	for index, edit := range edits {
		start, err := utf16Offset(data, edit.Start)
		if err != nil {
			return "", fmt.Errorf("edit %d start: %w", index, err)
		}
		end, err := utf16Offset(data, edit.End)
		if err != nil {
			return "", fmt.Errorf("edit %d end: %w", index, err)
		}
		if end < start {
			return "", fmt.Errorf("edit %d range ends before it starts", index)
		}
		spans[index] = span{start: start, end: end, text: edit.NewText, order: index}
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].order < spans[j].order
	})
	var output bytes.Buffer
	cursor := 0
	for index, edit := range spans {
		if edit.start < cursor {
			return "", errors.New("workspace text edits overlap")
		}
		if index > 0 && edit.start == spans[index-1].start && (edit.end != edit.start || spans[index-1].end != spans[index-1].start) {
			return "", errors.New("workspace text edits overlap")
		}
		output.Write(data[cursor:edit.start])
		output.WriteString(edit.text)
		cursor = edit.end
		if output.Len() > maxTextEditBytes {
			return "", errors.New("workspace edit result exceeds 8 MiB")
		}
	}
	output.Write(data[cursor:])
	if output.Len() > maxTextEditBytes {
		return "", errors.New("workspace edit result exceeds 8 MiB")
	}
	if len(edits) == 0 {
		return string(data), nil
	}
	current, err := os.Lstat(resolved)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, current) {
		return "", errors.New("workspace edit target changed before write")
	}
	temporary, err := os.CreateTemp(filepath.Dir(resolved), ".gork-lsp-edit-*")
	if err != nil {
		return "", fmt.Errorf("create workspace edit temporary file: %w", err)
	}
	tempPath := temporary.Name()
	defer os.Remove(tempPath)
	if err = temporary.Chmod(opened.Mode().Perm()); err == nil {
		_, err = temporary.Write(output.Bytes())
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("write workspace edit: %w", err)
	}
	current, err = os.Lstat(resolved)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, current) {
		return "", errors.New("workspace edit target changed before replace")
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close workspace edit target: %w", err)
	}
	if err := atomicReplace(tempPath, resolved); err != nil {
		return "", fmt.Errorf("replace workspace edit target: %w", err)
	}
	return output.String(), nil
}

func utf16Offset(text []byte, position TextPosition) (int, error) {
	if position.Line < 0 || position.Character < 0 {
		return 0, errors.New("position must be non-negative")
	}
	start := 0
	for line := 0; line < position.Line; line++ {
		newline := bytes.IndexByte(text[start:], '\n')
		if newline < 0 {
			return 0, errors.New("line is past end of document")
		}
		start += newline + 1
	}
	end := len(text)
	if newline := bytes.IndexByte(text[start:], '\n'); newline >= 0 {
		end = start + newline
	}
	if end > start && text[end-1] == '\r' {
		end--
	}
	units := 0
	for offset := start; offset < end; {
		if units == position.Character {
			return offset, nil
		}
		r, size := utf8.DecodeRune(text[offset:end])
		next := units + 1
		if r > 0xffff {
			next++
		}
		if position.Character > units && position.Character < next {
			return 0, errors.New("character splits a UTF-16 surrogate pair")
		}
		units, offset = next, offset+size
	}
	if units == position.Character {
		return end, nil
	}
	return 0, errors.New("character is past end of line")
}
