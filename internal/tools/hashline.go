package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type hashlineConfig struct {
	scheme             string
	hashLen, chunkSize int
}

func defaultHashlineConfig() hashlineConfig {
	return hashlineConfig{scheme: "chunk", hashLen: 3, chunkSize: 8}
}

func newHashlineConfig(scheme string, hashLen, chunkSize int) (hashlineConfig, error) {
	config := hashlineConfig{scheme: scheme, hashLen: hashLen, chunkSize: chunkSize}
	if scheme != "chunk" && scheme != "content_only" {
		return hashlineConfig{}, fmt.Errorf("unknown hashline scheme %q: expected chunk or content_only", scheme)
	}
	if hashLen < 1 || hashLen > 4 {
		return hashlineConfig{}, fmt.Errorf("hashline hash_len must be 1..=4, got %d", hashLen)
	}
	if scheme == "chunk" && chunkSize < 1 {
		return hashlineConfig{}, errors.New("hashline chunk_size must be greater than zero")
	}
	return config, nil
}

// ConfigureFileToolset swaps the standard file mutation surface for anchored tools.
func (r *Registry) ConfigureFileToolset(mode, scheme string, hashLen, chunkSize int) error {
	if mode == "standard" {
		return nil
	}
	if mode != "hashline" {
		return fmt.Errorf("unknown file toolset %q", mode)
	}
	config, err := newHashlineConfig(scheme, hashLen, chunkSize)
	if err != nil {
		return err
	}
	tools := []Tool{
		&hashlineReadTool{ws: r.readFile.ws, config: config},
		&hashlineEditTool{ws: r.readFile.ws, approver: r.approver, rewind: r.rewind, config: config},
		&hashlineGrepTool{ws: r.readFile.ws, config: config},
	}
	if _, err := r.Replace([]string{"read_file", "grep", "search_replace", "write_file", "edit_file"}, tools); err != nil {
		return err
	}
	r.mu.Lock()
	r.fileToolset, r.hashline = mode, config
	r.mu.Unlock()
	return nil
}

type hashlineAnchor struct {
	line           int
	local, context string
}

func (a hashlineAnchor) String() string {
	if a.context == "" {
		return fmt.Sprintf("%d:%s", a.line, a.local)
	}
	return fmt.Sprintf("%d:%s:%s", a.line, a.local, a.context)
}

func hashlineLines(content string) []string { return strings.Split(content, "\n") }

func fnv1a(data []byte) uint32 {
	hash := uint32(2166136261)
	for _, value := range data {
		hash ^= uint32(value)
		hash *= 16777619
	}
	return hash
}

func hashlineLineHash(line string) uint32 {
	hash, whitespace := uint32(2166136261), false
	for _, value := range []byte(strings.TrimSpace(line)) {
		isWhitespace := value == ' ' || value == '\t' || value == '\n' || value == '\r' || value == '\v' || value == '\f'
		if isWhitespace {
			if whitespace {
				continue
			}
			value, whitespace = ' ', true
		} else {
			whitespace = false
		}
		hash ^= uint32(value)
		hash *= 16777619
	}
	return hash
}

func encodeHash(hash uint32, length int) string {
	result := make([]byte, length)
	for index := range result {
		result[index] = byte((hash>>uint(index*8))%26) + 'a'
	}
	return string(result)
}

func (c hashlineConfig) anchors(lines []string) []hashlineAnchor {
	var contexts []string
	if c.scheme == "chunk" {
		contexts = make([]string, (len(lines)+c.chunkSize-1)/c.chunkSize)
		for chunk := range contexts {
			hash := fnv1a([]byte("chunk"))
			for _, line := range lines[chunk*c.chunkSize : min((chunk+1)*c.chunkSize, len(lines))] {
				hash ^= hashlineLineHash(line)
				hash *= 16777619
			}
			contexts[chunk] = encodeHash(hash, c.hashLen)
		}
	}
	anchors := make([]hashlineAnchor, len(lines))
	for index, line := range lines {
		anchors[index] = hashlineAnchor{line: index + 1, local: encodeHash(hashlineLineHash(line), c.hashLen)}
		if c.scheme == "chunk" {
			anchors[index].context = contexts[index/c.chunkSize]
		}
	}
	return anchors
}

func renderHashlines(lines []string, config hashlineConfig, start, end int) string {
	anchors := config.anchors(lines)
	start, end = max(0, start), min(len(lines), end)
	var output strings.Builder
	for index := start; index < end; index++ {
		fmt.Fprintf(&output, "%s→%s\n", anchors[index], lines[index])
	}
	return strings.TrimRight(output.String(), "\n")
}

func renderHashlineRegions(lines []string, config hashlineConfig, regions [][2]int) string {
	if len(regions) == 0 {
		regions = append(regions, [2]int{0, min(20, len(lines))})
	}
	sort.Slice(regions, func(i, j int) bool { return regions[i][0] < regions[j][0] })
	windows := make([][2]int, 0, len(regions))
	for _, region := range regions {
		window := [2]int{max(0, region[0]-3), min(len(lines), max(region[1], region[0]+1)+3)}
		if len(windows) > 0 && window[0] <= windows[len(windows)-1][1] {
			windows[len(windows)-1][1] = max(windows[len(windows)-1][1], window[1])
		} else {
			windows = append(windows, window)
		}
	}
	parts := make([]string, 0, len(windows))
	for _, window := range windows {
		parts = append(parts, renderHashlines(lines, config, window[0], window[1]))
	}
	return strings.Join(parts, "\n... lines not shown ...\n")
}

type hashlineReadTool struct {
	ws     *workspace.Workspace
	config hashlineConfig
}

func (*hashlineReadTool) WorkspaceBound() bool { return true }

func (t *hashlineReadTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{Type: "function", Name: "hashline_read",
		Description: "Read a UTF-8 text file with stable LINE:LOCAL:CONTEXT anchors for hashline_edit.",
		Parameters: objectSchema(map[string]any{
			"target_file": map[string]any{"type": "string"},
			"offset":      map[string]any{"type": "integer"},
			"limit":       map[string]any{"type": "integer", "minimum": 1, "maximum": 1000},
		}, "target_file")}
}

func (t *hashlineReadTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		TargetFile string `json:"target_file"`
		Path       string `json:"path"`
		Offset     *int   `json:"offset"`
		Limit      int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode hashline_read arguments: %w", err)
	}
	if args.TargetFile == "" {
		args.TargetFile = args.Path
	}
	if args.TargetFile == "" {
		return "", errors.New("target_file is required")
	}
	path, err := t.ws.Resolve(args.TargetFile)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %q: %w", args.TargetFile, err)
	}
	if len(data) > maxReadBytes || !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
		return "", fmt.Errorf("file %q is too large or is not UTF-8 text", args.TargetFile)
	}
	lines := hashlineLines(strings.ReplaceAll(string(data), "\r\n", "\n"))
	start := 1
	if args.Offset != nil {
		start = *args.Offset
		if start < 0 {
			start = len(lines) + start + 1
		}
	}
	start = max(1, start)
	limit := args.Limit
	if limit < 1 {
		limit = 1000
	}
	limit = min(limit, 1000)
	if start > len(lines) {
		return "", fmt.Errorf("invalid line offset %d for %d-line file", start, len(lines))
	}
	return renderHashlines(lines, t.config, start-1, min(len(lines), start-1+limit)), nil
}

type hashlineGrepTool struct {
	ws     *workspace.Workspace
	config hashlineConfig
}

func (*hashlineGrepTool) WorkspaceBound() bool { return true }

func (t *hashlineGrepTool) Definition() api.ToolDefinition {
	definition := (&grepTool{ws: t.ws}).Definition()
	definition.Name = "hashline_grep"
	definition.Description = "Search file contents and add hashline anchors to matching and context lines."
	return definition
}

var grepOutputLine = regexp.MustCompile(`^(.*)([:\-])([0-9]+)([:\-])(.*)$`)

func (t *hashlineGrepTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	output, err := (&grepTool{ws: t.ws}).Execute(ctx, raw)
	if err != nil || output == "no matches" {
		return output, err
	}
	cache := make(map[string][]hashlineAnchor)
	lines := strings.Split(output, "\n")
	for index, line := range lines {
		match := grepOutputLine.FindStringSubmatch(line)
		if len(match) != 6 {
			continue
		}
		lineNumber, parseErr := strconv.Atoi(match[3])
		if parseErr != nil || lineNumber < 1 {
			continue
		}
		path := match[1]
		anchors, ok := cache[path]
		if !ok {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				continue
			}
			anchors = t.config.anchors(hashlineLines(strings.ReplaceAll(string(data), "\r\n", "\n")))
			cache[path] = anchors
		}
		if lineNumber <= len(anchors) {
			lines[index] = match[1] + match[2] + anchors[lineNumber-1].String() + match[4] + match[5]
		}
	}
	return strings.Join(lines, "\n"), nil
}

type hashlineEdit struct {
	Op        string `json:"op"`
	Anchor    string `json:"anchor"`
	EndAnchor string `json:"end_anchor"`
	Content   string `json:"content"`
}

type hashlineEditTool struct {
	ws       *workspace.Workspace
	approver Approver
	rewind   *mutationCheckpoint
	config   hashlineConfig
}

func (*hashlineEditTool) WorkspaceBound() bool { return true }

func (t *hashlineEditTool) Definition() api.ToolDefinition {
	edit := map[string]any{"type": "object", "properties": map[string]any{
		"op":     map[string]any{"type": "string", "enum": []string{"replace", "insert_after", "write"}},
		"anchor": map[string]any{"type": "string"}, "end_anchor": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"},
	}, "required": []string{"op", "content"}, "additionalProperties": false}
	return api.ToolDefinition{Type: "function", Name: "hashline_edit",
		Description: "Atomically apply anchored replace, insert_after, or whole-file write operations. All anchors are validated before any edit.",
		Parameters: objectSchema(map[string]any{
			"file_path": map[string]any{"type": "string"},
			"edits":     map[string]any{"type": "array", "items": edit, "minItems": 1},
		}, "file_path", "edits")}
}

type resolvedHashlineEdit struct {
	index, start, end int
	lines             []string
}

func parseHashlineAnchor(value string) (int, string, string, error) {
	if before, _, found := strings.Cut(value, "→"); found {
		value = before
	} else if before, _, found := strings.Cut(value, "->"); found {
		value = before
	}
	parts := strings.Split(value, ":")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" {
		return 0, "", "", fmt.Errorf("malformed anchor %q", value)
	}
	line, err := strconv.Atoi(parts[0])
	if err != nil || line < 1 {
		return 0, "", "", fmt.Errorf("malformed anchor %q", value)
	}
	for _, part := range parts[1:] {
		for _, character := range []byte(part) {
			if character < 'a' || character > 'z' {
				return 0, "", "", fmt.Errorf("malformed anchor %q", value)
			}
		}
	}
	context := ""
	if len(parts) == 3 {
		context = parts[2]
	}
	return line, parts[1], context, nil
}

func (c hashlineConfig) validateAnchor(value string, lines []string) (int, error) {
	line, local, context, err := parseHashlineAnchor(value)
	if err != nil {
		return 0, err
	}
	if line > len(lines) {
		return 0, fmt.Errorf("anchor %q is out of range for %d-line file", value, len(lines))
	}
	current := c.anchors(lines)[line-1]
	if local != current.local || c.scheme == "chunk" && context != current.context {
		return 0, fmt.Errorf("anchor %q is stale; current anchor is %q", value, current.String())
	}
	return line - 1, nil
}

func hashlineContentLines(content string, emptyIsLine bool) []string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if content == "" {
		if emptyIsLine {
			return []string{""}
		}
		return nil
	}
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}

func (t *hashlineEditTool) resolve(edits []hashlineEdit, lines []string) ([]resolvedHashlineEdit, error) {
	resolved := make([]resolvedHashlineEdit, 0, len(edits))
	for index, edit := range edits {
		switch edit.Op {
		case "replace":
			start, err := t.config.validateAnchor(edit.Anchor, lines)
			if err != nil {
				return nil, fmt.Errorf("edit %d: %w; no edits were applied", index+1, err)
			}
			end := start
			if edit.EndAnchor != "" {
				end, err = t.config.validateAnchor(edit.EndAnchor, lines)
				if err != nil {
					return nil, fmt.Errorf("edit %d: %w; no edits were applied", index+1, err)
				}
			}
			if end < start {
				return nil, fmt.Errorf("edit %d: end_anchor precedes anchor; no edits were applied", index+1)
			}
			resolved = append(resolved, resolvedHashlineEdit{index: index, start: start, end: end + 1, lines: hashlineContentLines(edit.Content, false)})
		case "insert_after":
			position := 0
			if edit.Anchor == "EOF" {
				position = len(lines)
				if len(lines) > 1 && lines[len(lines)-1] == "" {
					position--
				}
			} else if edit.Anchor != "0:" {
				line, err := t.config.validateAnchor(edit.Anchor, lines)
				if err != nil {
					return nil, fmt.Errorf("edit %d: %w; no edits were applied", index+1, err)
				}
				position = line + 1
			}
			resolved = append(resolved, resolvedHashlineEdit{index: index, start: position, end: position, lines: hashlineContentLines(edit.Content, true)})
		case "write":
			return nil, errors.New("write must be the only edit in a batch")
		default:
			return nil, fmt.Errorf("edit %d: unsupported operation %q", index+1, edit.Op)
		}
	}
	return resolved, validateHashlineOverlaps(resolved)
}

func validateHashlineOverlaps(edits []resolvedHashlineEdit) error {
	for left := range edits {
		for right := left + 1; right < len(edits); right++ {
			a, b := edits[left], edits[right]
			overlaps := a.start < a.end && b.start < b.end && a.start < b.end && b.start < a.end
			inside := a.start == a.end && b.start <= a.start && a.start < b.end || b.start == b.end && a.start <= b.start && b.start < a.end
			if overlaps || inside {
				return fmt.Errorf("edits %d and %d overlap; no edits were applied", a.index+1, b.index+1)
			}
		}
	}
	return nil
}

func decodeHashlineEdits(raw json.RawMessage) (string, []hashlineEdit, error) {
	var input struct {
		FilePath string          `json:"file_path"`
		Edits    json.RawMessage `json:"edits"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", nil, err
	}
	editsRaw := input.Edits
	if len(editsRaw) > 0 && editsRaw[0] == '"' {
		var encoded string
		if err := json.Unmarshal(editsRaw, &encoded); err != nil {
			return "", nil, err
		}
		editsRaw = []byte(encoded)
	}
	var edits []hashlineEdit
	if len(editsRaw) > 0 && editsRaw[0] == '{' {
		var edit hashlineEdit
		if err := json.Unmarshal(editsRaw, &edit); err != nil {
			return "", nil, err
		}
		edits = []hashlineEdit{edit}
	} else if err := json.Unmarshal(editsRaw, &edits); err != nil {
		return "", nil, err
	}
	if input.FilePath == "" || len(edits) == 0 {
		return "", nil, errors.New("file_path and at least one edit are required")
	}
	return input.FilePath, edits, nil
}

func (t *hashlineEditTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	filePath, edits, err := decodeHashlineEdits(raw)
	if err != nil {
		return "", fmt.Errorf("decode hashline_edit arguments: %w", err)
	}
	path, err := t.ws.Resolve(filePath)
	if err != nil {
		return "", err
	}
	data, readErr := os.ReadFile(path)
	writeOnly := len(edits) == 1 && edits[0].Op == "write"
	if readErr != nil && (!writeOnly || !errors.Is(readErr, os.ErrNotExist)) {
		return "", fmt.Errorf("read %q: %w", filePath, readErr)
	}
	if len(data) > maxWriteBytes || !utf8.Valid(data) {
		return "", fmt.Errorf("file %q is too large or is not UTF-8", filePath)
	}
	hasCRLF := strings.Contains(string(data), "\r\n")
	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	newContent := ""
	var regions [][2]int
	if writeOnly {
		newContent = strings.ReplaceAll(edits[0].Content, "\r\n", "\n")
	} else {
		if len(edits) == 1 && edits[0].Op == "write" {
			return "", errors.New("write must be the only edit in a batch")
		}
		lines := hashlineLines(normalized)
		resolved, err := t.resolve(edits, lines)
		if err != nil {
			return "", err
		}
		topDown := append([]resolvedHashlineEdit(nil), resolved...)
		sort.Slice(topDown, func(i, j int) bool {
			return topDown[i].start < topDown[j].start || topDown[i].start == topDown[j].start && topDown[i].index < topDown[j].index
		})
		shift := 0
		for _, edit := range topDown {
			start := edit.start + shift
			regions = append(regions, [2]int{start, start + len(edit.lines)})
			shift += len(edit.lines) - (edit.end - edit.start)
		}
		sort.Slice(resolved, func(i, j int) bool {
			return resolved[i].start > resolved[j].start || resolved[i].start == resolved[j].start && resolved[i].index > resolved[j].index
		})
		for _, edit := range resolved {
			lines = append(lines[:edit.start], append(edit.lines, lines[edit.end:]...)...)
		}
		newContent = strings.Join(lines, "\n")
	}
	if len(newContent) > maxWriteBytes {
		return "", fmt.Errorf("edited content exceeds %d bytes", maxWriteBytes)
	}
	if hasCRLF {
		newContent = strings.ReplaceAll(newContent, "\n", "\r\n")
	}
	if err := t.approver.Approve(ctx, "hashline_edit", fmt.Sprintf("%s (%d edit(s))", t.ws.Relative(path), len(edits))); err != nil {
		return "", err
	}
	if err := t.rewind.before(filePath); err != nil {
		return "", fmt.Errorf("checkpoint before hashline edit: %w", err)
	}
	current, currentErr := os.ReadFile(path)
	if readErr == nil && (currentErr != nil || !bytes.Equal(current, data)) || errors.Is(readErr, os.ErrNotExist) && !errors.Is(currentErr, os.ErrNotExist) {
		t.rewind.cancel(filePath)
		return "", errors.New("file changed after anchor validation; no edits were applied")
	}
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	if err := atomicWrite(path, []byte(newContent), mode); err != nil {
		t.rewind.cancel(filePath)
		return "", err
	}
	if err := t.rewind.after(filePath); err != nil {
		return "", fmt.Errorf("checkpoint after hashline edit: %w", err)
	}
	freshLines := hashlineLines(strings.ReplaceAll(newContent, "\r\n", "\n"))
	return fmt.Sprintf("applied %d edit(s) to %s\n%s", len(edits), t.ws.Relative(path), renderHashlineRegions(freshLines, t.config, regions)), nil
}
