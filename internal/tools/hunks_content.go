package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxHunkContentBytes = 1 << 20

var lfsPointerPrefix = []byte("version https://git-lfs.github.com/spec/v1\n")

type FileContentView struct {
	Status  string  `json:"status"`
	ByteLen *int    `json:"byteLen,omitempty"`
	Content *string `json:"content,omitempty"`
}

type HunkFileData struct {
	Hunks           []Hunk          `json:"hunks"`
	Baseline        FileContentView `json:"baseline"`
	Current         FileContentView `json:"current"`
	BaselineContent *string         `json:"baselineContent,omitempty"`
	CurrentContent  *string         `json:"currentContent,omitempty"`
}

type FileContentEntry struct {
	Path        string          `json:"path"`
	Baseline    FileContentView `json:"baseline"`
	Current     FileContentView `json:"current"`
	IsAgentFile bool            `json:"isAgentFile"`
	Staged      bool            `json:"staged"`
}

func (t *HunkTracker) FileData(ctx context.Context, path, source string) (HunkFileData, error) {
	if strings.TrimSpace(path) == "" {
		return HunkFileData{}, errors.New("path is required")
	}
	path, err := t.entryRelativePath(path)
	if err != nil {
		return HunkFileData{}, err
	}
	hunks, err := t.Hunks(ctx, path, source)
	if err != nil {
		return HunkFileData{}, err
	}
	baseline := t.baselineContent(ctx, path)
	current := t.currentContent(path)
	return HunkFileData{
		Hunks: hunks, Baseline: baseline, Current: current,
		BaselineContent: baseline.Content, CurrentContent: current.Content,
	}, nil
}

func (t *HunkTracker) AllFileContents(ctx context.Context) ([]FileContentEntry, error) {
	t.syncHead(ctx)
	paths, err := t.changedPaths(ctx)
	if err != nil {
		return nil, err
	}
	staged := t.stagedPaths(ctx)
	entries := make([]FileContentEntry, 0, len(paths))
	for _, path := range paths {
		entries = append(entries, FileContentEntry{
			Path: path, Baseline: t.baselineContent(ctx, path), Current: t.currentContent(path),
			IsAgentFile: t.isAgentFile(path), Staged: staged[path],
		})
	}
	return entries, nil
}

func (t *HunkTracker) baselineContent(ctx context.Context, path string) FileContentView {
	command := exec.CommandContext(ctx, "git", "ls-tree", "-z", "HEAD", "--", path)
	command.Dir = t.ws.Root()
	output, err := command.Output()
	if err != nil || len(output) == 0 {
		return contentMissing()
	}
	metadata, _, ok := bytes.Cut(bytes.TrimSuffix(output, []byte{0}), []byte{'\t'})
	fields := strings.Fields(string(metadata))
	if !ok || len(fields) < 3 {
		return contentMissing()
	}
	if fields[0] == "120000" {
		return contentStatus("symlink", nil)
	}
	if fields[1] != "blob" {
		return contentStatus("binary", nil)
	}
	sizeCommand := exec.CommandContext(ctx, "git", "cat-file", "-s", fields[2])
	sizeCommand.Dir = t.ws.Root()
	sizeOutput, err := sizeCommand.Output()
	if err != nil {
		return contentMissing()
	}
	size, err := strconv.Atoi(strings.TrimSpace(string(sizeOutput)))
	if err != nil {
		return contentMissing()
	}
	if size > maxHunkContentBytes {
		return contentStatus("tooLarge", &size)
	}
	contentCommand := exec.CommandContext(ctx, "git", "cat-file", "blob", fields[2])
	contentCommand.Dir = t.ws.Root()
	data, err := contentCommand.Output()
	if err != nil {
		return contentMissing()
	}
	return classifyHunkContent(data)
}

func (t *HunkTracker) currentContent(path string) FileContentView {
	resolved, err := t.ws.ResolveEntry(path)
	if err != nil {
		return contentMissing()
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return contentMissing()
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return contentStatus("symlink", nil)
	}
	size := int(info.Size())
	if !info.Mode().IsRegular() {
		return contentStatus("binary", &size)
	}
	if size > maxHunkContentBytes {
		return contentStatus("tooLarge", &size)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return contentMissing()
	}
	return classifyHunkContent(data)
}

func classifyHunkContent(data []byte) FileContentView {
	size := len(data)
	if size > maxHunkContentBytes {
		return contentStatus("tooLarge", &size)
	}
	if size < 1024 && bytes.HasPrefix(data, lfsPointerPrefix) {
		return contentStatus("lfsPointer", &size)
	}
	if bytes.IndexByte(data[:min(size, 8000)], 0) >= 0 || !utf8.Valid(data) {
		return contentStatus("binary", &size)
	}
	content := string(data)
	return FileContentView{Status: "full", ByteLen: &size, Content: &content}
}

func contentMissing() FileContentView { return FileContentView{Status: "missing"} }

func contentStatus(status string, size *int) FileContentView {
	return FileContentView{Status: status, ByteLen: size}
}

func (t *HunkTracker) changedPaths(ctx context.Context) ([]string, error) {
	seen := make(map[string]bool)
	for _, args := range [][]string{
		{"diff", "--name-only", "-z", "--"},
		{"diff", "--cached", "--name-only", "-z", "--"},
		{"ls-files", "--others", "--exclude-standard", "-z", "--"},
	} {
		command := exec.CommandContext(ctx, "git", args...)
		command.Dir = t.ws.Root()
		output, err := command.Output()
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				return nil, fmt.Errorf("git changed paths: %w", err)
			}
		}
		for _, path := range bytes.Split(output, []byte{0}) {
			if len(path) > 0 {
				seen[string(path)] = true
			}
		}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}
