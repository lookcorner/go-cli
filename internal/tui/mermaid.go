package tui

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxMermaidStatements = 128
	maxMermaidLabelWidth = 24
	maxMermaidNodes      = 128
	maxMermaidRelations  = 512
	maxMermaidSource     = 64 << 10
)

type mermaidNode struct {
	id    string
	label string
}

type mermaidRelation struct {
	from  mermaidNode
	to    mermaidNode
	label string
	arrow string
	note  string
}

type mermaidPieSlice struct {
	label string
	value float64
}

type mermaidTimelineEntry struct {
	period  string
	events  []string
	section string
}

type mermaidJourneyTask struct {
	name    string
	score   int
	actors  []string
	section string
}

type mermaidGanttTask struct {
	name     string
	section  string
	start    time.Time
	duration int
}

type mermaidPacketBlock struct {
	start uint64
	end   uint64
	label string
}

var mermaidOperators = []string{
	"||--o{", "||--|{", "}o--o{", "}o..o{", "}o--||", "}|..|{",
	"<|--", "<-->", "--|>", "-.->", "-->>", "->>", "--x", "--o", "-x", "==>", "-->", "<--",
	"..|>", "..>", "*--", "o--", "---",
}

func renderMermaid(source string, width int, theme themePalette) ([]string, bool) {
	if width < 5 {
		return nil, false
	}
	source = sanitizeTerminalText(source)
	if len(source) > maxMermaidSource {
		return nil, false
	}
	if mermaidFirstToken(source) == "packet-beta" {
		return renderMermaidPacket(source, width, theme)
	}
	statements, complete := mermaidStatements(source)
	if !complete || len(statements) < 1 {
		return nil, false
	}
	header := strings.Fields(strings.ToLower(statements[0]))
	if len(header) == 0 {
		return nil, false
	}
	if header[0] == "pie" {
		return renderMermaidPie(statements, width, theme)
	}
	if header[0] == "timeline" {
		return renderMermaidTimeline(statements, width, theme)
	}
	if header[0] == "journey" {
		return renderMermaidJourney(statements, width, theme)
	}
	if header[0] == "gantt" {
		return renderMermaidGantt(statements, width, theme)
	}
	var relations []mermaidRelation
	var nodes []mermaidNode
	var parsed bool
	switch header[0] {
	case "graph", "flowchart", "statediagram", "statediagram-v2", "classdiagram", "erdiagram":
		relations, nodes, parsed = parseMermaidRelations(statements[1:], header[0])
	case "sequencediagram":
		relations, nodes, parsed = parseMermaidSequence(statements[1:])
	default:
		return nil, false
	}
	if !parsed || len(relations) == 0 && len(nodes) == 0 {
		return nil, false
	}
	lines := []string{ansiDim + mermaidFit("◇ mermaid", width) + ansiReset}
	for _, relation := range relations {
		if relation.note != "" {
			lines = append(lines, ansiDim+mermaidFit(relation.note, width)+ansiReset)
			continue
		}
		for _, line := range renderMermaidRelation(relation, width) {
			lines = append(lines, theme.code+line+ansiReset)
		}
	}
	for _, node := range nodes {
		for _, line := range mermaidBox(node.label, min(max(width, 4), maxMermaidLabelWidth+4)) {
			lines = append(lines, theme.code+line+ansiReset)
		}
	}
	return lines, true
}

func renderMermaidPacket(source string, width int, theme themePalette) ([]string, bool) {
	title := ""
	blocks := make([]mermaidPacketBlock, 0)
	var lastEnd uint64
	hasLast := false
	foundHeader := false
	statements := 0
	for _, raw := range strings.Split(source, "\n") {
		statement := strings.TrimSpace(raw)
		if statement == "" || strings.HasPrefix(statement, "%%") {
			continue
		}
		statements++
		if statements > maxMermaidStatements {
			return nil, false
		}
		if !foundHeader {
			if strings.Fields(statement)[0] != "packet-beta" {
				return nil, false
			}
			foundHeader = true
			continue
		}
		if value, ok := strings.CutPrefix(statement, "title "); ok {
			if value = strings.TrimSpace(value); value != "" {
				title = value
			}
			continue
		}
		rawRange, rawLabel, ok := strings.Cut(statement, ":")
		start, end, valid := mermaidPacketRange(rawRange)
		label, labelOK := mermaidPacketLabel(rawLabel)
		if !ok || !valid || !labelOK || hasLast && start != lastEnd+1 {
			return nil, false
		}
		for start <= end {
			if len(blocks) == maxMermaidRelations {
				return nil, false
			}
			rowEnd := (start/32+1)*32 - 1
			blockEnd := min(end, rowEnd)
			blocks = append(blocks, mermaidPacketBlock{start: start, end: blockEnd, label: label})
			start = blockEnd + 1
		}
		lastEnd = end
		hasLast = true
	}
	if len(blocks) == 0 {
		return nil, false
	}
	lines := []string{ansiDim + mermaidFit("◇ mermaid packet", width) + ansiReset}
	if title != "" {
		lines = append(lines, theme.heading+mermaidFit(title, width)+ansiReset)
	}
	var lastRow uint64
	hasRow := false
	for _, block := range blocks {
		row := block.start / 32
		if !hasRow || row != lastRow {
			if hasRow {
				lines = append(lines, "")
			}
			lastRow = row
			hasRow = true
		}
		bits := strconv.FormatUint(block.start, 10)
		if block.end != block.start {
			bits += "-" + strconv.FormatUint(block.end, 10)
		}
		lines = append(lines, theme.code+mermaidFit(bits+" │ "+block.label, width)+ansiReset)
	}
	return lines, true
}

func mermaidFirstToken(source string) string {
	for _, raw := range strings.Split(source, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "%%") {
			continue
		}
		if fields := strings.Fields(line); len(fields) > 0 {
			return fields[0]
		}
	}
	return ""
}

func mermaidPacketRange(value string) (uint64, uint64, bool) {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) < 1 || len(parts) > 2 {
		return 0, 0, false
	}
	start, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 32)
	if err != nil {
		return 0, 0, false
	}
	end := start
	if len(parts) == 2 {
		end, err = strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 32)
		if err != nil {
			return 0, 0, false
		}
	}
	if end < start {
		return 0, 0, false
	}
	return start, end, true
}

func mermaidPacketLabel(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
		return value[1 : len(value)-1], true
	}
	return value, value != ""
}

func renderMermaidJourney(statements []string, width int, theme themePalette) ([]string, bool) {
	title := ""
	section := ""
	tasks := make([]mermaidJourneyTask, 0, len(statements)-1)
	for _, statement := range statements[1:] {
		if value, ok := strings.CutPrefix(statement, "title "); ok {
			title = mermaidCleanLabel(value)
			continue
		}
		if value, ok := strings.CutPrefix(statement, "section "); ok {
			section = mermaidCleanLabel(value)
			continue
		}
		parts := strings.Split(statement, ":")
		values := make([]string, 0, len(parts))
		for _, part := range parts {
			if value := strings.TrimSpace(part); value != "" {
				values = append(values, value)
			}
		}
		if len(values) < 2 {
			return nil, false
		}
		score, err := strconv.Atoi(values[1])
		if err != nil {
			return nil, false
		}
		actors := []string(nil)
		if len(values) > 2 {
			for _, actor := range strings.Split(strings.Join(values[2:], ": "), ",") {
				if actor = mermaidCleanLabel(actor); actor != "" {
					actors = append(actors, actor)
				}
			}
		}
		tasks = append(tasks, mermaidJourneyTask{name: mermaidCleanLabel(values[0]), score: score, actors: actors, section: section})
	}
	if len(tasks) == 0 {
		return nil, false
	}
	lines := []string{ansiDim + mermaidFit("◇ mermaid journey", width) + ansiReset}
	if title != "" {
		lines = append(lines, theme.heading+mermaidFit(title, width)+ansiReset)
	}
	lastSection := ""
	for _, task := range tasks {
		if task.section != "" && task.section != lastSection {
			lines = append(lines, theme.heading+mermaidFit(task.section, width)+ansiReset)
		}
		lastSection = task.section
		lines = append(lines, renderMermaidJourneyTask(task, width, theme))
	}
	return lines, true
}

func renderMermaidJourneyTask(task mermaidJourneyTask, width int, theme themePalette) string {
	text := fmt.Sprintf("%s [%d]", task.name, task.score)
	if len(task.actors) > 0 {
		text += " · " + strings.Join(task.actors, ", ")
	}
	return theme.code + mermaidFit("• "+text, width) + ansiReset
}

func renderMermaidGantt(statements []string, width int, theme themePalette) ([]string, bool) {
	title := ""
	section := ""
	tasks := make([]mermaidGanttTask, 0, len(statements)-1)
	completed := make(map[string]mermaidGanttTask)
	for _, statement := range statements[1:] {
		if value, ok := strings.CutPrefix(statement, "title "); ok {
			title = mermaidCleanLabel(value)
			continue
		}
		if strings.HasPrefix(statement, "dateFormat ") {
			continue
		}
		if value, ok := strings.CutPrefix(statement, "section "); ok {
			section = mermaidCleanLabel(value)
			continue
		}
		name, rawSpec, ok := strings.Cut(statement, ":")
		parts := strings.Split(rawSpec, ",")
		spec := make([]string, 0, len(parts))
		for _, part := range parts {
			if value := strings.TrimSpace(part); value != "" {
				spec = append(spec, value)
			}
		}
		if !ok || len(spec) < 3 {
			return nil, false
		}
		duration, ok := mermaidGanttDuration(spec[2])
		if !ok {
			return nil, false
		}
		start := time.Time{}
		if id, found := strings.CutPrefix(spec[1], "after "); found {
			previous, exists := completed[strings.TrimSpace(id)]
			if !exists {
				return nil, false
			}
			start = previous.start.AddDate(0, 0, previous.duration)
		} else {
			var valid bool
			start, valid = mermaidGanttDate(spec[1])
			if !valid {
				return nil, false
			}
		}
		task := mermaidGanttTask{name: mermaidCleanLabel(name), section: section, start: start, duration: duration}
		if task.name == "" {
			return nil, false
		}
		tasks = append(tasks, task)
		completed[spec[0]] = task
	}
	if len(tasks) == 0 {
		return nil, false
	}
	lines := []string{ansiDim + mermaidFit("◇ mermaid gantt", width) + ansiReset}
	if title != "" {
		lines = append(lines, theme.heading+mermaidFit(title, width)+ansiReset)
	}
	lastSection := ""
	for _, task := range tasks {
		if task.section != "" && task.section != lastSection {
			lines = append(lines, theme.heading+mermaidFit(task.section, width)+ansiReset)
		}
		lastSection = task.section
		end := task.start.AddDate(0, 0, task.duration)
		text := fmt.Sprintf("• %s  %s → %s", task.name, task.start.Format("2006-01-02"), end.Format("2006-01-02"))
		lines = append(lines, theme.code+mermaidFit(text, width)+ansiReset)
	}
	return lines, true
}

func mermaidGanttDate(value string) (time.Time, bool) {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	values := [3]int{}
	for index, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil {
			return time.Time{}, false
		}
		values[index] = parsed
	}
	return time.Date(values[0], time.Month(values[1]), values[2], 0, 0, 0, 0, time.UTC), true
}

func mermaidGanttDuration(value string) (int, bool) {
	if len(value) < 2 {
		return 0, false
	}
	count, err := strconv.Atoi(strings.TrimSpace(value[:len(value)-1]))
	if err != nil {
		return 0, false
	}
	switch value[len(value)-1] {
	case 'd', 'D':
		return count, true
	case 'w', 'W':
		return count * 7, true
	default:
		return 0, false
	}
}

func renderMermaidTimeline(statements []string, width int, theme themePalette) ([]string, bool) {
	title := ""
	section := ""
	sections := make([]string, 0)
	entries := make([]mermaidTimelineEntry, 0, len(statements)-1)
	for _, statement := range statements[1:] {
		if strings.HasPrefix(statement, "#") {
			continue
		}
		if value, ok := strings.CutPrefix(statement, "title "); ok {
			title = mermaidCleanLabel(value)
			continue
		}
		if value, ok := strings.CutPrefix(statement, "section "); ok {
			section = mermaidCleanLabel(value)
			known := false
			for _, name := range sections {
				known = known || name == section
			}
			if section != "" && !known {
				sections = append(sections, section)
			}
			continue
		}
		if value, ok := strings.CutPrefix(statement, ": "); ok {
			if event := mermaidCleanLabel(value); event != "" && len(entries) > 0 {
				entries[len(entries)-1].events = append(entries[len(entries)-1].events, event)
			}
			continue
		}
		period, event, found := strings.Cut(statement, ":")
		period = mermaidCleanLabel(period)
		if period == "" {
			continue
		}
		entry := mermaidTimelineEntry{period: period, section: section}
		if found {
			if event = mermaidCleanLabel(event); event != "" {
				entry.events = []string{event}
			}
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return nil, false
	}
	if len(sections) > 0 {
		grouped := make([]mermaidTimelineEntry, 0, len(entries))
		for _, name := range sections {
			for _, entry := range entries {
				if entry.section == name {
					grouped = append(grouped, entry)
				}
			}
		}
		entries = grouped
	}
	if len(entries) == 0 {
		return nil, false
	}
	lines := []string{ansiDim + mermaidFit("◇ mermaid timeline", width) + ansiReset}
	if title != "" {
		lines = append(lines, theme.heading+mermaidFit(title, width)+ansiReset)
	}
	lastSection := ""
	for index, entry := range entries {
		if entry.section != "" && entry.section != lastSection {
			lines = append(lines, theme.heading+mermaidFit(entry.section, width)+ansiReset)
		}
		lastSection = entry.section
		marker := "├─ "
		if index == len(entries)-1 {
			marker = "└─ "
		}
		lines = append(lines, theme.code+mermaidFit(marker+entry.period, width)+ansiReset)
		for eventIndex, event := range entry.events {
			eventMarker := "│  ├─ "
			if eventIndex == len(entry.events)-1 {
				eventMarker = "│  └─ "
			}
			lines = append(lines, theme.code+mermaidFit(eventMarker+event, width)+ansiReset)
		}
	}
	return lines, true
}

func renderMermaidPie(statements []string, width int, theme themePalette) ([]string, bool) {
	showData := false
	for _, field := range strings.Fields(statements[0])[1:] {
		if strings.EqualFold(field, "showData") {
			showData = true
		}
	}
	title := ""
	slices := make([]mermaidPieSlice, 0, len(statements)-1)
	total := 0.0
	for _, statement := range statements[1:] {
		if strings.HasPrefix(strings.ToLower(statement), "title ") {
			title = mermaidCleanLabel(statement[len("title "):])
			continue
		}
		label, rawValue, ok := strings.Cut(statement, ":")
		label = mermaidCleanLabel(label)
		value, err := strconv.ParseFloat(strings.TrimSpace(rawValue), 64)
		if !ok || label == "" || err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return nil, false
		}
		slices = append(slices, mermaidPieSlice{label: label, value: value})
		total += value
	}
	if len(slices) == 0 || total <= 0 || math.IsInf(total, 0) {
		return nil, false
	}
	sort.SliceStable(slices, func(i, j int) bool { return slices[i].value > slices[j].value })
	lines := []string{ansiDim + mermaidFit("◇ mermaid pie", width) + ansiReset}
	if title != "" {
		lines = append(lines, theme.heading+mermaidFit(title, width)+ansiReset)
	}
	for _, slice := range slices {
		percent := slice.value / total * 100
		label := slice.label
		if showData {
			label += " [" + strconv.FormatFloat(slice.value, 'f', -1, 64) + "]"
		}
		percentText := fmt.Sprintf("%d%%", int(math.Round(percent)))
		lines = append(lines, theme.code+mermaidFit(label, width)+ansiReset)
		available := max(width-displayWidth(percentText)-1, 0)
		barWidth := min(available, max(0, int(math.Round(percent/100*float64(available)))))
		metric := padDisplayRight(strings.Repeat("█", barWidth), available)
		if available > 0 {
			metric += " "
		}
		lines = append(lines, theme.code+mermaidFit(metric+percentText, width)+ansiReset)
	}
	return lines, true
}

func mermaidStatements(source string) ([]string, bool) {
	result := make([]string, 0)
	for _, raw := range strings.Split(source, "\n") {
		for _, part := range mermaidLineStatements(raw) {
			if part != "" {
				if len(result) == maxMermaidStatements {
					return nil, false
				}
				result = append(result, part)
			}
		}
	}
	return result, true
}

func mermaidLineStatements(line string) []string {
	result := make([]string, 0, 1)
	start, depth := 0, 0
	quoted := false
	flush := func(end int) {
		if value := strings.TrimSpace(line[start:end]); value != "" {
			result = append(result, value)
		}
		start = end + 1
	}
	for index := 0; index < len(line); index++ {
		switch line[index] {
		case '"':
			quoted = !quoted
		case '[', '(':
			if !quoted {
				depth++
			}
		case '{':
			if !quoted && strings.Contains(line[index+1:], "}") {
				depth++
			}
		case ']', ')', '}':
			if !quoted && depth > 0 {
				depth--
			}
		case '%':
			if !quoted && depth == 0 && index+1 < len(line) && line[index+1] == '%' {
				flush(index)
				return result
			}
		case ';':
			if !quoted && depth == 0 {
				flush(index)
			}
		}
	}
	flush(len(line))
	return result
}

func parseMermaidRelations(statements []string, kind string) ([]mermaidRelation, []mermaidNode, bool) {
	relations := make([]mermaidRelation, 0)
	nodes := make(map[string]mermaidNode)
	used := make(map[string]bool)
	inEntityBlock := false
	for _, statement := range statements {
		lower := strings.ToLower(strings.TrimSpace(statement))
		if kind == "classdiagram" || kind == "erdiagram" {
			if inEntityBlock {
				if strings.Contains(statement, "}") {
					inEntityBlock = false
				}
				continue
			}
			if strings.HasSuffix(strings.TrimSpace(statement), "{") {
				if node, ok := mermaidStandaloneNode(statement, kind); ok {
					nodes[node.id] = mermaidPreferNode(nodes[node.id], node)
					if len(nodes) > maxMermaidNodes {
						return nil, nil, false
					}
				}
				inEntityBlock = true
				continue
			}
		}
		if lower == "end" || strings.HasPrefix(lower, "direction ") ||
			strings.HasPrefix(lower, "classdef ") || strings.HasPrefix(lower, "style ") ||
			strings.HasPrefix(lower, "linkstyle ") || strings.HasPrefix(lower, "click ") ||
			strings.HasPrefix(lower, "subgraph ") {
			continue
		}
		if strings.HasPrefix(lower, "state ") && strings.Contains(lower, " as ") {
			left, right, _ := strings.Cut(strings.TrimSpace(statement[len("state "):]), " as ")
			node := mermaidNode{id: strings.TrimSpace(right), label: mermaidCleanLabel(left)}
			if node.id != "" && node.label != "" {
				nodes[node.id] = mermaidPreferNode(nodes[node.id], node)
				if len(nodes) > maxMermaidNodes {
					return nil, nil, false
				}
			}
			continue
		}
		operator, position := mermaidOperator(statement)
		if position < 0 {
			if node, ok := mermaidStandaloneNode(statement, kind); ok {
				nodes[node.id] = mermaidPreferNode(nodes[node.id], node)
				if len(nodes) > maxMermaidNodes {
					return nil, nil, false
				}
			}
			continue
		}
		remaining := statement
		for position >= 0 {
			left := strings.TrimSpace(remaining[:position])
			right := strings.TrimSpace(remaining[position+len(operator):])
			label := ""
			if split := strings.LastIndex(left, " -- "); split >= 0 {
				label, left = strings.TrimSpace(left[split+4:]), strings.TrimSpace(left[:split])
			}
			if strings.HasPrefix(right, "|") {
				if end := strings.Index(right[1:], "|"); end >= 0 {
					label, right = strings.TrimSpace(right[1:end+1]), strings.TrimSpace(right[end+2:])
				}
			}
			nextOperator, nextPosition := mermaidOperator(right)
			target, next := right, ""
			if nextPosition >= 0 {
				target = strings.TrimSpace(right[:nextPosition])
				next = target + " " + right[nextPosition:]
			}
			if split := mermaidLabelColon(target); split >= 0 {
				if label == "" {
					label = strings.TrimSpace(target[split+1:])
				}
				target = strings.TrimSpace(target[:split])
			}
			fromNodes, fromOK := mermaidParseNodes(left)
			toNodes, toOK := mermaidParseNodes(target)
			if !fromOK || !toOK {
				return nil, nil, false
			}
			for _, from := range fromNodes {
				for _, to := range toNodes {
					if len(relations) == maxMermaidRelations {
						return nil, nil, false
					}
					nodes[from.id] = mermaidPreferNode(nodes[from.id], from)
					nodes[to.id] = mermaidPreferNode(nodes[to.id], to)
					if len(nodes) > maxMermaidNodes {
						return nil, nil, false
					}
					used[from.id], used[to.id] = true, true
					relations = append(relations, mermaidRelation{
						from: from, to: to, label: mermaidCleanLabel(label), arrow: mermaidArrow(operator),
					})
				}
			}
			if next == "" {
				break
			}
			remaining, operator, position = next, nextOperator, strings.Index(next, nextOperator)
		}
	}
	for index := range relations {
		if node, ok := nodes[relations[index].from.id]; ok {
			relations[index].from = node
		}
		if node, ok := nodes[relations[index].to.id]; ok {
			relations[index].to = node
		}
	}
	standalone := make([]mermaidNode, 0)
	for id, node := range nodes {
		if !used[id] {
			standalone = append(standalone, node)
		}
	}
	sort.Slice(standalone, func(i, j int) bool { return standalone[i].id < standalone[j].id })
	return relations, standalone, true
}

func parseMermaidSequence(statements []string) ([]mermaidRelation, []mermaidNode, bool) {
	relations := make([]mermaidRelation, 0)
	participants := make(map[string]mermaidNode)
	used := make(map[string]bool)
	for _, statement := range statements {
		lower := strings.ToLower(statement)
		if strings.HasPrefix(lower, "participant ") || strings.HasPrefix(lower, "actor ") {
			declaration := strings.TrimSpace(statement[strings.IndexByte(statement, ' ')+1:])
			id, label := declaration, declaration
			if left, right, ok := strings.Cut(declaration, " as "); ok {
				id, label = strings.TrimSpace(left), strings.TrimSpace(right)
			}
			if id != "" {
				participants[id] = mermaidNode{id: id, label: mermaidCleanLabel(label)}
				if len(participants) > maxMermaidNodes {
					return nil, nil, false
				}
			}
			continue
		}
		if strings.HasPrefix(lower, "note ") {
			if len(relations) == maxMermaidRelations {
				return nil, nil, false
			}
			relations = append(relations, mermaidRelation{note: strings.TrimSpace(statement)})
			continue
		}
		if strings.HasPrefix(lower, "loop ") || strings.HasPrefix(lower, "alt ") ||
			strings.HasPrefix(lower, "opt ") || strings.HasPrefix(lower, "par ") ||
			strings.HasPrefix(lower, "critical ") || lower == "else" || lower == "end" {
			if len(relations) == maxMermaidRelations {
				return nil, nil, false
			}
			relations = append(relations, mermaidRelation{note: strings.TrimSpace(statement)})
			continue
		}
		operator, position := mermaidSequenceOperator(statement)
		if position < 0 {
			continue
		}
		fromID := strings.TrimSpace(statement[:position])
		right := strings.TrimSpace(statement[position+len(operator):])
		toID, label := right, ""
		if split := strings.Index(right, ":"); split >= 0 {
			toID, label = strings.TrimSpace(right[:split]), strings.TrimSpace(right[split+1:])
		}
		if fromID == "" || toID == "" {
			continue
		}
		from := participants[fromID]
		if from.id == "" {
			from = mermaidNode{id: fromID, label: fromID}
			participants[fromID] = from
		}
		to := participants[toID]
		if to.id == "" {
			to = mermaidNode{id: toID, label: toID}
			participants[toID] = to
		}
		if len(participants) > maxMermaidNodes || len(relations) == maxMermaidRelations {
			return nil, nil, false
		}
		used[fromID], used[toID] = true, true
		relations = append(relations, mermaidRelation{
			from: from, to: to, label: mermaidCleanLabel(label), arrow: mermaidArrow(operator),
		})
	}
	standalone := make([]mermaidNode, 0)
	for id, node := range participants {
		if !used[id] {
			standalone = append(standalone, node)
		}
	}
	sort.Slice(standalone, func(i, j int) bool { return standalone[i].id < standalone[j].id })
	return relations, standalone, true
}

func mermaidOperator(statement string) (string, int) {
	depth := 0
	quoted := false
	for position := 0; position < len(statement); position++ {
		switch statement[position] {
		case '"':
			quoted = !quoted
			continue
		case '[', '(', '{':
			if !quoted {
				depth++
			}
		case ']', ')', '}':
			if !quoted && depth > 0 {
				depth--
			}
		}
		if quoted || depth > 0 {
			continue
		}
		best := ""
		for _, operator := range mermaidOperators {
			if strings.HasPrefix(statement[position:], operator) && len(operator) > len(best) {
				best = operator
			}
		}
		if best != "" {
			return best, position
		}
	}
	return "", -1
}

func mermaidSequenceOperator(statement string) (string, int) {
	for _, operator := range []string{"-->>", "->>", "--x", "-x", "--)", "-)", "-->", "->"} {
		if position := strings.Index(statement, operator); position >= 0 {
			return operator, position
		}
	}
	return "", -1
}

func mermaidStandaloneNode(statement, kind string) (mermaidNode, bool) {
	value := strings.TrimSpace(statement)
	if kind == "classdiagram" && strings.HasPrefix(strings.ToLower(value), "class ") {
		value = strings.TrimSpace(value[len("class "):])
	}
	if open := strings.Index(value, "{"); open >= 0 && (kind == "classdiagram" || kind == "erdiagram") {
		value = strings.TrimSpace(value[:open])
	}
	return mermaidParseNode(value)
}

func mermaidLabelColon(value string) int {
	depth := 0
	quoted := false
	for index, char := range value {
		switch char {
		case '"':
			quoted = !quoted
		case '[', '(', '{':
			if !quoted {
				depth++
			}
		case ']', ')', '}':
			if !quoted && depth > 0 {
				depth--
			}
		case ':':
			if !quoted && depth == 0 {
				return index
			}
		}
	}
	return -1
}

func mermaidPreferNode(current, candidate mermaidNode) mermaidNode {
	if current.id != "" && candidate.label == candidate.id && current.label != current.id {
		return current
	}
	return candidate
}

func mermaidParseNodes(value string) ([]mermaidNode, bool) {
	parts := strings.Split(value, " & ")
	if len(parts) > maxMermaidNodes {
		return nil, false
	}
	nodes := make([]mermaidNode, 0, len(parts))
	for _, part := range parts {
		if node, ok := mermaidParseNode(part); ok {
			nodes = append(nodes, node)
		}
	}
	return nodes, true
}

func mermaidParseNode(value string) (mermaidNode, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return mermaidNode{}, false
	}
	if value == "[*]" {
		return mermaidNode{id: value, label: "●"}, true
	}
	id, label := value, value
	for _, pair := range [][2]string{{"[(", ")]"}, {"((", "))"}, {"[", "]"}, {"(", ")"}, {"{", "}"}} {
		if open := strings.Index(value, pair[0]); open > 0 && strings.HasSuffix(value, pair[1]) {
			id = strings.TrimSpace(value[:open])
			label = value[open+len(pair[0]) : len(value)-len(pair[1])]
			break
		}
	}
	if fields := strings.Fields(id); len(fields) > 0 {
		id = fields[0]
	}
	id, label = mermaidCleanLabel(id), mermaidCleanLabel(label)
	return mermaidNode{id: id, label: label}, id != "" && label != ""
}

func mermaidCleanLabel(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	replacer := strings.NewReplacer("<br/>", " ", "<br>", " ", "&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">")
	return strings.Join(strings.Fields(replacer.Replace(value)), " ")
}

func mermaidArrow(operator string) string {
	switch {
	case strings.Contains(operator, "<|"):
		return "◁━━"
	case strings.Contains(operator, "<") && strings.Contains(operator, ">"):
		return "◀─▶"
	case strings.HasPrefix(operator, "<"):
		return "◀──"
	case strings.Contains(operator, "*"):
		return "◆──"
	case strings.Contains(operator, "o--"):
		return "◇──"
	case strings.Contains(operator, "."):
		return "┈┈▶"
	case strings.Contains(operator, "=="):
		return "━━▶"
	case operator == "---" || operator == "--":
		return "───"
	case strings.Contains(operator, "x"):
		return "──×"
	case strings.HasSuffix(operator, "o"):
		return "──○"
	default:
		return "──▶"
	}
}

func renderMermaidRelation(relation mermaidRelation, width int) []string {
	width = max(width, 1)
	from, to := relation.from.label, relation.to.label
	connector := " " + relation.arrow + " "
	if relation.label != "" {
		connector = " ─" + mermaidFit(relation.label, 16) + relation.arrow + " "
	}
	nodeWidth := min(maxMermaidLabelWidth+4, max((width-displayWidth(connector))/2, 5))
	left, right := mermaidBox(from, nodeWidth), mermaidBox(to, nodeWidth)
	if displayWidth(left[1])+displayWidth(connector)+displayWidth(right[1]) <= width {
		gap := strings.Repeat(" ", displayWidth(connector))
		return []string{left[0] + gap + right[0], left[1] + connector + right[1], left[2] + gap + right[2]}
	}
	lines := append([]string(nil), left...)
	lines = append(lines, "  │")
	if relation.label != "" {
		lines = append(lines, "  "+mermaidFit(relation.label, max(width-2, 1)))
	}
	lines = append(lines, "  "+mermaidVerticalArrow(relation.arrow))
	return append(lines, right...)
}

func mermaidVerticalArrow(arrow string) string {
	switch {
	case strings.Contains(arrow, "×"):
		return "×"
	case strings.Contains(arrow, "◁"), strings.Contains(arrow, "◀"):
		return "▲"
	case arrow == "───":
		return "│"
	default:
		return "▼"
	}
}

func mermaidBox(label string, width int) []string {
	width = max(width, 4)
	label = mermaidFit(label, width-4)
	inner := max(displayWidth(label)+2, 3)
	return []string{
		"┌" + strings.Repeat("─", inner) + "┐",
		"│ " + padDisplayRight(label, inner-2) + " │",
		"└" + strings.Repeat("─", inner) + "┘",
	}
}

func mermaidFit(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if displayWidth(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	result := fitInputLine([]rune(value), width-1)
	return result + "…"
}
