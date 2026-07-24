package tui

import (
	"sort"
	"strings"
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
	statements, complete := mermaidStatements(source)
	if !complete || len(statements) < 1 {
		return nil, false
	}
	header := strings.Fields(strings.ToLower(statements[0]))
	if len(header) == 0 {
		return nil, false
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
