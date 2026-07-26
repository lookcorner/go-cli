package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
)

const (
	toolCompactLines = 20
	toolCompactRunes = 4_000
	toolExpandLimit  = 256
)

type toolStartedEvent struct{ call api.ToolCall }

type toolFinishedEvent struct {
	call   api.ToolCall
	result tools.ExecutionResult
	err    error
}

type toolVerbKind string

const (
	toolVerbFile         toolVerbKind = "file"
	toolVerbSkill        toolVerbKind = "skill"
	toolVerbSearch       toolVerbKind = "search"
	toolVerbDir          toolVerbKind = "dir"
	toolVerbWebFetch     toolVerbKind = "web_fetch"
	toolVerbWebSearch    toolVerbKind = "web_search"
	toolVerbMemorySearch toolVerbKind = "memory_search"
	toolVerbSubagent     toolVerbKind = "subagent"
)

type toolVerbMember struct {
	kind      toolVerbKind
	failed    bool
	full      string
	citations []string
}

type toolVerbGroup struct {
	prefix      string
	members     []toolVerbMember
	expandIndex int
	foldIndex   int
	rendered    bool
}

type collapsedEditMember struct {
	path           string
	added, removed int
	full           string
}

type collapsedEditGroup struct {
	prefix      string
	members     []collapsedEditMember
	expandIndex int
	foldIndex   int
	rendered    bool
}

type toolFold struct {
	start, end int
	collapsed  string
	full       string
	expanded   bool
}

func (b *Bridge) ToolStarted(call api.ToolCall) {
	b.send(toolStartedEvent{call: call})
}

func (b *Bridge) ToolFinished(call api.ToolCall, result tools.ExecutionResult, err error) {
	b.send(toolFinishedEvent{call: call, result: result, err: err})
}

func (m *model) finishTool(event toolFinishedEvent) {
	m.finishThought()
	images := m.inlineImagesFor(event.result.Images)
	full, _ := renderToolBlock(event.call, event.result, images, event.err, false)
	if m.groupToolVerbs {
		if kind, ok := classifyToolVerb(event.call.Name, event.call.Arguments); ok {
			m.finishCollapsedEditGroup()
			m.addToolVerbMember(toolVerbMember{
				kind: kind, failed: event.err != nil, full: full, citations: event.result.Citations,
			})
			m.status = "tool finished: " + event.call.Name
			return
		}
	}
	m.finishToolVerbGroup()
	if m.collapsedEditBlocks {
		if member, ok := collapsedEditMemberFor(event.call.Name, event.call.Arguments, event.result.Output, event.err != nil, full); ok {
			m.addCollapsedEditMember(member)
			m.status = "tool finished: " + event.call.Name
			return
		}
	}
	m.finishCollapsedEditGroup()
	compact, folded := renderToolBlock(event.call, event.result, images, event.err, true)
	start := m.transcript.Len()
	m.appendToolDisplay(compact)
	if folded {
		m.rememberToolExpansion(full)
		if !m.minimal {
			m.rememberToolFold(start, full)
		}
	}
	if m.minimal {
		m.minimalFlushTo = m.transcript.Len()
	}
	if event.err != nil {
		m.status = "tool failed: " + event.call.Name
	} else {
		m.status = "tool finished: " + event.call.Name
	}
}

func (m *model) rememberToolExpansion(full string) int {
	m.toolExpand = append(m.toolExpand, full)
	if len(m.toolExpand) > toolExpandLimit {
		copy(m.toolExpand, m.toolExpand[len(m.toolExpand)-toolExpandLimit:])
		m.toolExpand = m.toolExpand[:toolExpandLimit]
	}
	return len(m.toolExpand) - 1
}

func (m *model) rememberToolFold(start int, full string) int {
	fold := toolFold{
		start: start, end: m.transcript.Len(),
		collapsed: m.transcript.String()[start:], full: full,
	}
	m.toolFolds = append(m.toolFolds, fold)
	if len(m.toolFolds) > toolExpandLimit {
		m.toolFolds = m.toolFolds[len(m.toolFolds)-toolExpandLimit:]
	}
	return len(m.toolFolds) - 1
}

func (m *model) addToolVerbMember(member toolVerbMember) {
	if m.toolVerbGroup == nil {
		m.toolVerbGroup = &toolVerbGroup{prefix: m.transcript.String(), expandIndex: -1, foldIndex: -1}
	}
	group := m.toolVerbGroup
	previousScroll := m.scroll
	group.members = append(group.members, member)
	label := toolVerbGroupLabel(group.members)
	full := toolVerbGroupExpansion(group.members)
	if m.minimal {
		return
	}
	if group.rendered {
		m.transcript.Reset()
		m.transcript.WriteString(group.prefix)
	}
	m.appendToolDisplay(label)
	group.rendered = true
	if group.expandIndex < 0 {
		group.expandIndex = m.rememberToolExpansion(full)
		group.foldIndex = m.rememberToolFold(len(group.prefix), full)
	} else if group.expandIndex < len(m.toolExpand) {
		m.toolExpand[group.expandIndex] = full
		m.updateToolFold(group.foldIndex, len(group.prefix), full, previousScroll)
	}
}

func (m *model) finishToolVerbGroup() {
	group := m.toolVerbGroup
	if group == nil {
		return
	}
	if m.minimal {
		m.appendToolDisplay(toolVerbGroupLabel(group.members))
		m.rememberToolExpansion(toolVerbGroupExpansion(group.members))
		m.minimalFlushTo = m.transcript.Len()
	}
	m.toolVerbGroup = nil
}

func (m *model) addCollapsedEditMember(member collapsedEditMember) {
	if group := m.collapsedEditGroup; group != nil && !sameEditPath(group.members[0].path, member.path, m.workspace) {
		m.finishCollapsedEditGroup()
	}
	if m.collapsedEditGroup == nil {
		m.collapsedEditGroup = &collapsedEditGroup{prefix: m.transcript.String(), expandIndex: -1, foldIndex: -1}
	}
	group := m.collapsedEditGroup
	previousScroll := m.scroll
	group.members = append(group.members, member)
	label := collapsedEditGroupLabel(group.members)
	full := collapsedEditGroupExpansion(group.members)
	if m.minimal {
		return
	}
	if group.rendered {
		m.transcript.Reset()
		m.transcript.WriteString(group.prefix)
	}
	m.appendToolDisplay(label)
	group.rendered = true
	if group.expandIndex < 0 {
		group.expandIndex = m.rememberToolExpansion(full)
		group.foldIndex = m.rememberToolFold(len(group.prefix), full)
	} else if group.expandIndex < len(m.toolExpand) {
		m.toolExpand[group.expandIndex] = full
		m.updateToolFold(group.foldIndex, len(group.prefix), full, previousScroll)
	}
}

func (m *model) updateToolFold(index, start int, full string, previousScroll int) {
	if index < 0 || index >= len(m.toolFolds) {
		return
	}
	fold := &m.toolFolds[index]
	oldFull := fold.full
	fold.start, fold.end = start, m.transcript.Len()
	current := m.transcript.String()[start:]
	fold.collapsed, fold.full = current, full
	if !m.respectManualFolds || !fold.expanded {
		fold.expanded = false
		return
	}
	replacement := toolFoldReplacement(current, full)
	width := m.transcriptRenderWidth()
	beforeLines := len(renderMarkdownTheme(oldFull, width, false, m.colors()))
	afterLines := len(renderMarkdownTheme(replacement, width, false, m.colors()))
	prefix := m.transcript.String()[:start]
	m.transcript.Reset()
	m.transcript.WriteString(prefix)
	m.transcript.WriteString(replacement)
	fold.end = m.transcript.Len()
	m.scroll = min(max(previousScroll+afterLines-beforeLines, 0), m.maxTranscriptScroll())
}

func (m *model) finishCollapsedEditGroup() {
	group := m.collapsedEditGroup
	if group == nil {
		return
	}
	if m.minimal {
		m.appendToolDisplay(collapsedEditGroupLabel(group.members))
		m.rememberToolExpansion(collapsedEditGroupExpansion(group.members))
		m.minimalFlushTo = m.transcript.Len()
	}
	m.collapsedEditGroup = nil
}

func (m *model) expandLastTool() {
	if !m.minimal {
		m.appendSystem("/expand is only available in minimal mode (--minimal)")
		m.status = "expand unavailable"
		return
	}
	if len(m.toolExpand) == 0 {
		m.status = "nothing folded to expand"
		return
	}
	last := len(m.toolExpand) - 1
	block := m.toolExpand[last]
	m.toolExpand = m.toolExpand[:last]
	m.appendSystem(block)
	m.minimalFlushTo = m.transcript.Len()
	m.status = "tool output expanded"
}

func (m *model) toggleVisibleToolFold() bool {
	if m.minimal || len(m.toolFolds) == 0 {
		return false
	}
	text := m.transcript.String()
	width := m.transcriptRenderWidth()
	total := len(renderMarkdownTheme(m.transcriptText(), width, false, m.colors()))
	bottom := max(total-m.scroll, 0)
	top := max(bottom-m.contentHeight(), 0)
	selected := -1
	selectedLine := -1
	for index := range m.toolFolds {
		fold := m.toolFolds[index]
		if fold.start < 0 || fold.end > len(text) || fold.start > fold.end {
			continue
		}
		line := len(renderMarkdownTheme(m.transcriptTextPrefix(fold.start), width, false, m.colors()))
		if line >= top && line < bottom && line >= selectedLine {
			selected, selectedLine = index, line
		}
	}
	if selected < 0 {
		return false
	}
	fold := &m.toolFolds[selected]
	replacement := fold.full
	if fold.expanded {
		replacement = fold.collapsed
	}
	replacement = toolFoldReplacement(text[fold.start:fold.end], replacement)
	beforeLines := len(renderMarkdownTheme(m.transcriptTextPrefix(fold.end), width, false, m.colors()))
	delta := len(replacement) - (fold.end - fold.start)
	m.transcript.Reset()
	m.transcript.WriteString(text[:fold.start])
	m.transcript.WriteString(replacement)
	m.transcript.WriteString(text[fold.end:])
	fold.end += delta
	fold.expanded = !fold.expanded
	for index := selected + 1; index < len(m.toolFolds); index++ {
		m.toolFolds[index].start += delta
		m.toolFolds[index].end += delta
	}
	for index := range m.transcriptMessages {
		if m.transcriptMessages[index].start >= fold.end-delta {
			m.transcriptMessages[index].start += delta
			m.transcriptMessages[index].offset += delta
		}
	}
	afterLines := len(renderMarkdownTheme(m.transcriptTextPrefix(fold.end), width, false, m.colors()))
	m.scroll = min(max(m.scroll+afterLines-beforeLines, 0), m.maxTranscriptScroll())
	if fold.expanded {
		m.status = "tool group expanded"
	} else {
		m.status = "tool group collapsed"
	}
	return true
}

func toolFoldReplacement(current, body string) string {
	leading := current[:len(current)-len(strings.TrimLeft(current, "\n"))]
	trailing := current[len(strings.TrimRight(current, "\n")):]
	return leading + strings.TrimSpace(body) + trailing
}

func renderToolBlock(call api.ToolCall, result tools.ExecutionResult, images []session.DisplayImage, toolErr error, compact bool) (string, bool) {
	output := result.Output
	if toolErr != nil {
		if strings.TrimSpace(output) != "" {
			output += "\n\n"
		}
		output += "ERROR: " + toolErr.Error()
	}
	return renderStoredToolBlock(session.DisplayTool{
		Name: call.Name, Arguments: call.Arguments, Output: output, Failed: toolErr != nil,
		ImageCount: len(images), Images: images,
	}, compact)
}

func renderStoredToolBlock(tool session.DisplayTool, compact bool) (string, bool) {
	title := "Tool"
	if tool.Failed {
		title = "Tool failed"
	}
	var sections []string
	folded := false
	if args := strings.TrimSpace(string(tool.Arguments)); args != "" && args != "{}" {
		if pretty, err := prettyJSON(args); err == nil {
			args = pretty
		}
		if compact {
			var cut bool
			args, cut = compactToolText(args)
			folded = folded || cut
		}
		sections = append(sections, "Arguments\n\n"+toolFence("json", args))
	}
	output := strings.TrimSpace(tool.Output)
	if output == "" {
		output = "(no text output)"
	}
	if compact {
		var cut bool
		output, cut = compactToolText(output)
		folded = folded || cut
	}
	sections = append(sections, "Result\n\n"+toolFence("text", output))
	if len(tool.Images) > 0 {
		lines := make([]string, 0, len(tool.Images))
		for _, image := range tool.Images {
			if image.KittyID > 0 && len(image.Data) > 0 {
				cols, rows := inlineImageCells(image.Width, image.Height, 12)
				lines = append(lines, toolFence(fmt.Sprintf("gork-image:%d:%d:%d", image.KittyID, cols, rows), kittyPlaceholderGrid(cols, rows)))
				continue
			}
			lines = append(lines, fmt.Sprintf("- %s · %dx%d · %d bytes", image.MediaType, image.Width, image.Height, image.Bytes))
		}
		sections = append(sections, "Images\n\n"+strings.Join(lines, "\n\n"))
	} else if tool.ImageCount > 0 {
		sections = append(sections, fmt.Sprintf("Images\n\n- %d image attachment(s)", tool.ImageCount))
	}
	return fmt.Sprintf("#### %s: `%s`\n\n%s", title, tool.Name, strings.Join(sections, "\n\n")), folded
}

func sessionDisplayTranscript(path, workspace string, collapsedEditBlocks, groupToolVerbs, showThinking bool, maxThoughtsWidth int, imageHooks ...func(*session.DisplayImage, string)) (string, []transcriptMessage, []string, []toolFold, error) {
	entries, err := session.DisplayTimeline(path)
	if err != nil {
		return "", nil, nil, nil, err
	}
	var text strings.Builder
	var messages []transcriptMessage
	var expands []string
	var folds []toolFold
	assistantOpen := false
	lastKind := ""
	var verbGroup []session.DisplayTool
	var editGroup []collapsedEditMember
	enrich := func(tool *session.DisplayTool) {
		for index := range tool.Images {
			for _, hook := range imageHooks {
				hook(&tool.Images[index], path)
			}
		}
	}
	separate := func() {
		if text.Len() > 0 {
			text.WriteString("\n\n")
		}
	}
	label := func(value, role string, at session.DisplayEntry) {
		start := text.Len()
		text.WriteString(value)
		messages = append(messages, transcriptMessage{start: start, offset: text.Len(), at: at.Time, role: role})
		text.WriteByte('\n')
	}
	writeTool := func(tool session.DisplayTool) {
		enrich(&tool)
		compact, folded := renderStoredToolBlock(tool, true)
		start := text.Len()
		text.WriteString(compact)
		if folded {
			full, _ := renderStoredToolBlock(tool, false)
			expands = appendBoundedExpansion(expands, full)
			folds = appendBoundedFold(folds, toolFold{
				start: start, end: text.Len(), collapsed: compact, full: full,
			})
		}
	}
	flushVerbGroup := func() {
		if len(verbGroup) == 0 {
			return
		}
		if lastKind != "" {
			text.WriteString("\n\n")
		}
		members := make([]toolVerbMember, 0, len(verbGroup))
		for _, tool := range verbGroup {
			enrich(&tool)
			full, _ := renderStoredToolBlock(tool, false)
			kind, _ := classifyToolVerb(tool.Name, tool.Arguments)
			members = append(members, toolVerbMember{kind: kind, failed: tool.Failed, full: full})
			members[len(members)-1].citations = tool.Citations
		}
		collapsed := toolVerbGroupLabel(members)
		full := toolVerbGroupExpansion(members)
		start := text.Len()
		text.WriteString(collapsed)
		expands = appendBoundedExpansion(expands, full)
		folds = appendBoundedFold(folds, toolFold{
			start: start, end: text.Len(), collapsed: collapsed, full: full,
		})
		verbGroup = nil
		lastKind = "tool"
	}
	flushEditGroup := func() {
		if len(editGroup) == 0 {
			return
		}
		if lastKind != "" {
			text.WriteString("\n\n")
		}
		collapsed := collapsedEditGroupLabel(editGroup)
		full := collapsedEditGroupExpansion(editGroup)
		start := text.Len()
		text.WriteString(collapsed)
		expands = appendBoundedExpansion(expands, full)
		folds = appendBoundedFold(folds, toolFold{
			start: start, end: text.Len(), collapsed: collapsed, full: full,
		})
		editGroup = nil
		lastKind = "tool"
	}
	for _, entry := range entries {
		switch entry.Kind {
		case "user":
			flushVerbGroup()
			flushEditGroup()
			if entry.Synthetic {
				assistantOpen = false
				lastKind = ""
				continue
			}
			separate()
			label("You", "user", entry)
			text.WriteString(displayPromptBody(entry))
			assistantOpen = false
			lastKind = "user"
		case "thought":
			if !showThinking {
				continue
			}
			flushVerbGroup()
			flushEditGroup()
			if !assistantOpen {
				separate()
				label("Gork", "assistant", entry)
				assistantOpen = true
			}
			if lastKind != "" {
				text.WriteString("\n\n")
			}
			text.WriteString("> Thinking\n>\n> ")
			text.WriteString(formatThought(entry.Text, maxThoughtsWidth))
			lastKind = "thought"
		case "assistant", "tool":
			if !assistantOpen {
				separate()
				label("Gork", "assistant", entry)
				assistantOpen = true
				lastKind = ""
			}
			if entry.Kind == "assistant" {
				flushVerbGroup()
				flushEditGroup()
				if lastKind == "tool" || lastKind == "thought" {
					text.WriteString("\n\n")
				}
				text.WriteString(entry.Text)
			} else if entry.Tool != nil {
				if groupToolVerbs {
					if _, ok := classifyToolVerb(entry.Tool.Name, entry.Tool.Arguments); ok {
						flushEditGroup()
						verbGroup = append(verbGroup, *entry.Tool)
						continue
					}
				}
				flushVerbGroup()
				if collapsedEditBlocks {
					full, _ := renderStoredToolBlock(*entry.Tool, false)
					if member, ok := collapsedEditMemberFor(entry.Tool.Name, entry.Tool.Arguments, entry.Tool.Output, entry.Tool.Failed, full); ok {
						if len(editGroup) > 0 && !sameEditPath(editGroup[0].path, member.path, workspace) {
							flushEditGroup()
						}
						editGroup = append(editGroup, member)
						continue
					}
				}
				flushEditGroup()
				if lastKind != "" {
					text.WriteString("\n\n")
				}
				writeTool(*entry.Tool)
			}
			lastKind = entry.Kind
		}
	}
	flushVerbGroup()
	flushEditGroup()
	rendered := strings.TrimSpace(text.String())
	trimmed := len(text.String()) - len(strings.TrimLeft(text.String(), " \t\r\n"))
	for index := range folds {
		folds[index].start -= trimmed
		folds[index].end -= trimmed
	}
	return rendered, messages, expands, folds, nil
}

func formatThought(value string, width int) string {
	width = normalizedThoughtWidth(width)
	runes := []rune(value)
	var text strings.Builder
	column := 0
	for index, char := range runes {
		charWidth := displayWidth(string(char))
		if char != '\n' && column > 0 && column+charWidth > width {
			text.WriteString("\n> ")
			column = 0
		}
		text.WriteRune(char)
		if char == '\n' {
			if index+1 < len(runes) {
				text.WriteString("> ")
			}
			column = 0
		} else {
			column += charWidth
		}
	}
	return text.String()
}

func appendBoundedExpansion(expands []string, full string) []string {
	expands = append(expands, full)
	if len(expands) > toolExpandLimit {
		expands = expands[len(expands)-toolExpandLimit:]
	}
	return expands
}

func appendBoundedFold(folds []toolFold, fold toolFold) []toolFold {
	folds = append(folds, fold)
	if len(folds) > toolExpandLimit {
		folds = folds[len(folds)-toolExpandLimit:]
	}
	return folds
}

func classifyToolVerb(name string, raw json.RawMessage) (toolVerbKind, bool) {
	switch name {
	case "read_file", "hashline_read":
		var args struct {
			TargetFile string `json:"target_file"`
			Path       string `json:"path"`
		}
		_ = json.Unmarshal(raw, &args)
		path := args.TargetFile
		if path == "" {
			path = args.Path
		}
		if strings.EqualFold(filepath.Base(path), "SKILL.md") {
			return toolVerbSkill, true
		}
		return toolVerbFile, true
	case "grep", "hashline_grep", "search_files":
		return toolVerbSearch, true
	case "list_dir", "list_files":
		return toolVerbDir, true
	case "web_fetch":
		return toolVerbWebFetch, true
	case "web_search":
		return toolVerbWebSearch, true
	case "memory_search":
		return toolVerbMemorySearch, true
	case "task":
		return toolVerbSubagent, true
	default:
		return "", false
	}
}

func toolVerbGroupLabel(members []toolVerbMember) string {
	type bucket struct {
		kind      toolVerbKind
		count     int
		citations map[string]bool
	}
	var buckets []bucket
	failed := 0
	for _, member := range members {
		index := -1
		for candidate := range buckets {
			if buckets[candidate].kind == member.kind {
				index = candidate
				break
			}
		}
		if index < 0 {
			buckets = append(buckets, bucket{kind: member.kind, citations: make(map[string]bool)})
			index = len(buckets) - 1
		}
		buckets[index].count++
		if member.kind == toolVerbWebSearch && !member.failed {
			for _, citation := range member.citations {
				if citation != "" {
					buckets[index].citations[citation] = true
				}
			}
		}
		if member.failed {
			failed++
		}
	}
	labels := make([]string, 0, len(buckets))
	for _, item := range buckets {
		verb, one, many := toolVerbWords(item.kind)
		count := item.count
		if len(item.citations) > 0 {
			count = len(item.citations)
		}
		noun := many
		if count == 1 {
			noun = one
		}
		labels = append(labels, fmt.Sprintf("%s %d %s", verb, count, noun))
	}
	result := strings.Join(labels, ", ")
	if failed > 0 {
		result += fmt.Sprintf(" · %d failed", failed)
	}
	return result
}

func toolVerbWords(kind toolVerbKind) (verb, one, many string) {
	switch kind {
	case toolVerbFile:
		return "Read", "file", "files"
	case toolVerbSkill:
		return "Read", "skill", "skills"
	case toolVerbSearch:
		return "Searched", "pattern", "patterns"
	case toolVerbDir:
		return "Listed", "dir", "dirs"
	case toolVerbWebFetch:
		return "Fetched", "website", "websites"
	case toolVerbWebSearch:
		return "Searched", "website", "websites"
	case toolVerbMemorySearch:
		return "Searched", "memory", "memories"
	default:
		return "Ran", "subagent", "subagents"
	}
}

func toolVerbGroupExpansion(members []toolVerbMember) string {
	blocks := make([]string, 0, len(members))
	for _, member := range members {
		blocks = append(blocks, member.full)
	}
	return strings.Join(blocks, "\n\n")
}

func collapsedEditSummary(name string, arguments json.RawMessage, output string, failed bool) (string, bool) {
	member, ok := collapsedEditMemberFor(name, arguments, output, failed, "")
	if !ok {
		return "", false
	}
	return collapsedEditGroupLabel([]collapsedEditMember{member}), true
}

func collapsedEditMemberFor(name string, arguments json.RawMessage, output string, failed bool, full string) (collapsedEditMember, bool) {
	if failed {
		return collapsedEditMember{}, false
	}
	path, added, removed, ok := editDiffstat(name, arguments, output)
	return collapsedEditMember{path: path, added: added, removed: removed, full: full}, ok
}

func collapsedEditGroupLabel(members []collapsedEditMember) string {
	added, removed := 0, 0
	for _, member := range members {
		added += member.added
		removed += member.removed
	}
	return fmt.Sprintf("Edit `%s` +%d/-%d", filepath.Base(members[0].path), added, removed)
}

func collapsedEditGroupExpansion(members []collapsedEditMember) string {
	blocks := make([]string, 0, len(members))
	for _, member := range members {
		blocks = append(blocks, member.full)
	}
	return strings.Join(blocks, "\n\n")
}

func sameEditPath(left, right, workspace string) bool {
	normalize := func(path string) string {
		if workspace != "" && !filepath.IsAbs(path) {
			path = filepath.Join(workspace, path)
		}
		return filepath.Clean(path)
	}
	return normalize(left) == normalize(right)
}

func editDiffstat(name string, raw json.RawMessage, output string) (string, int, int, bool) {
	switch name {
	case "edit_file":
		var args struct {
			Path       string `json:"path"`
			OldText    string `json:"old_text"`
			NewText    string `json:"new_text"`
			ReplaceAll bool   `json:"replace_all"`
		}
		if json.Unmarshal(raw, &args) != nil || strings.TrimSpace(args.Path) == "" || args.OldText == "" {
			return "", 0, 0, false
		}
		count := 1
		if args.ReplaceAll {
			count = replacementCount(output)
			if count == 0 {
				return "", 0, 0, false
			}
		}
		return args.Path, lineCount(args.NewText) * count, lineCount(args.OldText) * count, true
	case "search_replace":
		var args struct {
			FilePath   string `json:"file_path"`
			OldString  string `json:"old_string"`
			NewString  string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
		}
		if json.Unmarshal(raw, &args) != nil || strings.TrimSpace(args.FilePath) == "" || args.OldString == "" {
			return "", 0, 0, false
		}
		count := 1
		if args.ReplaceAll {
			count = replacementCount(output)
			if count == 0 {
				return "", 0, 0, false
			}
		}
		return args.FilePath, lineCount(args.NewString) * count, lineCount(args.OldString) * count, true
	case "hashline_edit":
		path, edits, err := decodeDisplayHashlineEdits(raw)
		if err != nil {
			return "", 0, 0, false
		}
		added, removed := 0, 0
		for _, edit := range edits {
			switch edit.Op {
			case "replace":
				start, ok := displayAnchorLine(edit.Anchor)
				if !ok {
					return "", 0, 0, false
				}
				end := start
				if edit.EndAnchor != "" {
					end, ok = displayAnchorLine(edit.EndAnchor)
					if !ok || end < start {
						return "", 0, 0, false
					}
				}
				added += lineCount(edit.Content)
				removed += end - start + 1
			case "insert_after":
				added += insertedLineCount(edit.Content)
			default:
				return "", 0, 0, false
			}
		}
		return path, added, removed, true
	default:
		return "", 0, 0, false
	}
}

type displayHashlineEdit struct {
	Op        string `json:"op"`
	Anchor    string `json:"anchor"`
	EndAnchor string `json:"end_anchor"`
	Content   string `json:"content"`
}

func decodeDisplayHashlineEdits(raw json.RawMessage) (string, []displayHashlineEdit, error) {
	var input struct {
		FilePath string          `json:"file_path"`
		Edits    json.RawMessage `json:"edits"`
	}
	if err := json.Unmarshal(raw, &input); err != nil || strings.TrimSpace(input.FilePath) == "" {
		return "", nil, fmt.Errorf("invalid hashline edit")
	}
	editsRaw := input.Edits
	if len(editsRaw) > 0 && editsRaw[0] == '"' {
		var encoded string
		if err := json.Unmarshal(editsRaw, &encoded); err != nil {
			return "", nil, err
		}
		editsRaw = []byte(encoded)
	}
	var edits []displayHashlineEdit
	if len(editsRaw) > 0 && editsRaw[0] == '{' {
		var edit displayHashlineEdit
		if err := json.Unmarshal(editsRaw, &edit); err != nil {
			return "", nil, err
		}
		edits = []displayHashlineEdit{edit}
	} else if err := json.Unmarshal(editsRaw, &edits); err != nil {
		return "", nil, err
	}
	if len(edits) == 0 {
		return "", nil, fmt.Errorf("missing hashline edits")
	}
	return input.FilePath, edits, nil
}

func displayAnchorLine(anchor string) (int, bool) {
	value, _, ok := strings.Cut(anchor, ":")
	if !ok {
		return 0, false
	}
	line, err := strconv.Atoi(value)
	return line, err == nil && line > 0
}

func replacementCount(output string) int {
	const suffix = " replacement(s)"
	before, _, ok := strings.Cut(output, suffix)
	if !ok {
		return 0
	}
	start := strings.LastIndex(before, "(")
	if start < 0 {
		return 0
	}
	count, err := strconv.Atoi(before[start+1:])
	if err != nil || count < 1 {
		return 0
	}
	return count
}

func lineCount(value string) int {
	if value == "" {
		return 0
	}
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.TrimSuffix(value, "\n")
	if value == "" {
		return 1
	}
	return strings.Count(value, "\n") + 1
}

func insertedLineCount(value string) int {
	if value == "" {
		return 1
	}
	return lineCount(value)
}

func displayPromptBody(entry session.DisplayEntry) string {
	if len(entry.Content) == 0 {
		return entry.Text
	}
	content := make([]string, 0, len(entry.Content))
	for _, part := range entry.Content {
		switch part.Type {
		case "text":
			content = append(content, part.Text)
		case "image":
			if strings.HasPrefix(part.URI, "http://") || strings.HasPrefix(part.URI, "https://") {
				content = append(content, "[Image: "+part.URI+"]")
			} else {
				content = append(content, "[Image]")
			}
		}
	}
	return strings.Join(content, "\n")
}

func prettyJSON(value string) (string, error) {
	var raw any
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return "", err
	}
	pretty, err := json.MarshalIndent(raw, "", "  ")
	return string(pretty), err
}

func compactToolText(value string) (string, bool) {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	lines := strings.Split(value, "\n")
	folded := false
	if len(lines) > toolCompactLines {
		lines = lines[:toolCompactLines]
		folded = true
	}
	value = strings.Join(lines, "\n")
	runes := []rune(value)
	if len(runes) > toolCompactRunes {
		value = string(runes[:toolCompactRunes])
		folded = true
	}
	if folded {
		value = strings.TrimRight(value, "\n") + "\n… output folded; use /expand or Ctrl-E in minimal mode"
	}
	return value, folded
}

func toolFence(language, value string) string {
	fence := "```"
	for strings.Contains(value, fence) {
		fence += "`"
	}
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "�")
	}
	return fence + language + "\n" + value + "\n" + fence
}
