package workspace

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultSearchMaxFiles   = 100
	defaultSearchMaxMatches = 1000
)

type ContentSearchRequest struct {
	Pattern          string
	CaseInsensitive  bool
	WholeWord        bool
	IsRegex          bool
	IncludeGlobs     []string
	ExcludeGlobs     []string
	MaxFiles         *int
	MaxMatches       *int
	RespectGitignore bool
}

type ContentMatch struct {
	Line       int    `json:"line"`
	Content    string `json:"content"`
	MatchStart *int   `json:"matchStart,omitempty"`
	MatchEnd   *int   `json:"matchEnd,omitempty"`
}

type ContentMatchFile struct {
	Name    string         `json:"name"`
	Path    string         `json:"path"`
	Matches []ContentMatch `json:"matches"`
}

type ContentSearchData struct {
	Files        []ContentMatchFile `json:"files"`
	TotalMatches int                `json:"totalMatches"`
	TotalFiles   int                `json:"totalFiles"`
	Truncated    bool               `json:"truncated"`
}

type ContentSearchBatch struct {
	ContentSearchData
	Done bool `json:"done"`
}

type ripgrepEvent struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
		Submatches []struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"submatches"`
	} `json:"data"`
}

// SearchContent runs a bounded ripgrep search and emits completed-file batches.
func SearchContent(ctx context.Context, root string, req ContentSearchRequest, emit func(ContentSearchBatch)) (ContentSearchData, error) {
	ws, err := Open(root)
	if err != nil {
		return ContentSearchData{}, err
	}
	maxFiles := searchLimit(req.MaxFiles, defaultSearchMaxFiles)
	maxMatches := searchLimit(req.MaxMatches, defaultSearchMaxMatches)
	cmd := exec.CommandContext(ctx, "rg", contentSearchArgs(req)...)
	cmd.Dir = ws.Root()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ContentSearchData{}, fmt.Errorf("capture ripgrep output: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return ContentSearchData{}, fmt.Errorf("start ripgrep: %w", err)
	}

	result := ContentSearchData{Files: []ContentMatchFile{}}
	var current *ContentMatchFile
	pending := []ContentMatchFile{}
	lastEmit := time.Now()
	truncated := false
	flushCurrent := func() {
		if current == nil || len(current.Matches) == 0 {
			current = nil
			return
		}
		result.Files = append(result.Files, *current)
		pending = append(pending, *current)
		current = nil
	}
	emitPending := func(done bool) {
		if emit == nil || (!done && len(pending) == 0) {
			return
		}
		emit(ContentSearchBatch{ContentSearchData: ContentSearchData{
			Files: pending, TotalMatches: result.TotalMatches, TotalFiles: len(result.Files), Truncated: truncated,
		}, Done: done})
		pending = []ContentMatchFile{}
		lastEmit = time.Now()
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	for scanner.Scan() {
		var event ripgrepEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		switch event.Type {
		case "begin":
			flushCurrent()
			path := event.Data.Path.Text
			if !filepath.IsAbs(path) {
				path = filepath.Join(ws.Root(), strings.TrimPrefix(path, "./"))
			}
			current = &ContentMatchFile{Name: filepath.Base(path), Path: path, Matches: []ContentMatch{}}
		case "match":
			if current == nil {
				continue
			}
			match := ContentMatch{Line: event.Data.LineNumber, Content: strings.TrimSuffix(event.Data.Lines.Text, "\n")}
			if len(event.Data.Submatches) > 0 {
				start, end := event.Data.Submatches[0].Start, event.Data.Submatches[0].End
				match.MatchStart, match.MatchEnd = &start, &end
			}
			current.Matches = append(current.Matches, match)
			result.TotalMatches++
		case "end":
			flushCurrent()
		}
		if len(result.Files) >= maxFiles || result.TotalMatches >= maxMatches {
			flushCurrent()
			truncated = true
			break
		}
		if len(pending) > 0 && time.Since(lastEmit) >= 50*time.Millisecond {
			emitPending(false)
		}
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return ContentSearchData{}, fmt.Errorf("read ripgrep output: %w", err)
	}
	if truncated {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ContentSearchData{}, ctx.Err()
	}
	if waitErr != nil && !truncated {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != 1 {
			return ContentSearchData{}, fmt.Errorf("run ripgrep: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
		}
	}
	flushCurrent()
	result.TotalFiles = len(result.Files)
	result.Truncated = truncated
	emitPending(true)
	return result, nil
}

func searchLimit(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return max(0, *value)
}

func contentSearchArgs(req ContentSearchRequest) []string {
	args := []string{
		"--json", "--line-number", "--glob", "!.git/**", "--glob", "!submodules/**", "--glob", "!vendor/**",
		"--max-filesize", "1M", "--max-count", "50", "--max-columns", "500", "--max-columns-preview",
	}
	if req.CaseInsensitive {
		args = append(args, "--ignore-case")
	}
	if !req.IsRegex {
		args = append(args, "--fixed-strings")
		if req.WholeWord {
			args = append(args, "--word-regexp")
		}
	}
	if !req.RespectGitignore {
		args = append(args, "--no-ignore")
	}
	for _, glob := range req.IncludeGlobs {
		args = append(args, "--glob", glob)
	}
	for _, glob := range req.ExcludeGlobs {
		args = append(args, "--glob", "!"+glob)
	}
	return append(args, "-e", req.Pattern, ".")
}
