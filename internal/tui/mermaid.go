package tui

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
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

type mermaidGitCommit struct {
	branch  string
	parents []int
	merge   bool
}

type mermaidSankeyLink struct {
	source string
	target string
	value  float64
}

type mermaidQuadrantPoint struct {
	label string
	x     float64
	y     float64
}

type mermaidRadarCurve struct {
	name   string
	values []float64
}

type mermaidXYAxis struct {
	title      string
	categories []string
	min        float64
	max        float64
	numeric    bool
}

type mermaidMindmapNode struct {
	label    string
	shape    string
	children []*mermaidMindmapNode
}

type mermaidBlockEdge struct {
	from string
	to   string
}

type mermaidKanbanColumn struct {
	title string
	tasks []mermaidKanbanTask
}

type mermaidKanbanTask struct {
	label    string
	assigned *string
	priority *string
	ticket   *string
}

type mermaidRequirementNode struct {
	name string
	kind string
	body []string
}

type mermaidRequirementRelation struct {
	from string
	to   string
	kind string
}

type mermaidC4Shape struct {
	alias    string
	kind     string
	label    string
	tech     string
	detail   string
	boundary string
}

type mermaidC4Boundary struct {
	alias string
	label string
	kind  string
}

type mermaidC4Relation struct {
	from  string
	to    string
	label string
	tech  string
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
	firstToken := mermaidFirstToken(source)
	if firstToken == "packet-beta" {
		return renderMermaidPacket(source, width, theme)
	}
	if firstToken == "gitGraph" {
		return renderMermaidGitGraph(source, width, theme)
	}
	if firstToken == "sankey-beta" {
		return renderMermaidSankey(source, width, theme)
	}
	if firstToken == "quadrantChart" {
		return renderMermaidQuadrant(source, width, theme)
	}
	if firstToken == "radar-beta" {
		return renderMermaidRadar(source, width, theme)
	}
	if firstToken == "xychart-beta" {
		return renderMermaidXYChart(source, width, theme)
	}
	if firstToken == "mindmap" {
		return renderMermaidMindmap(source, width, theme)
	}
	if firstToken == "block-beta" {
		return renderMermaidBlock(source, width, theme)
	}
	if firstToken == "kanban" {
		return renderMermaidKanban(source, width, theme)
	}
	if firstToken == "info" {
		return renderMermaidInfo(source, width, theme)
	}
	if firstToken == "requirementDiagram" {
		return renderMermaidRequirement(source, width, theme)
	}
	if strings.HasPrefix(firstToken, "C4") {
		return renderMermaidC4(source, width, theme)
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

func renderMermaidInfo(source string, width int, theme themePalette) ([]string, bool) {
	if mermaidFirstToken(source) != "info" {
		return nil, false
	}
	return []string{
		ansiDim + mermaidFit("◇ mermaid info", width) + ansiReset,
		theme.heading + mermaidFit("v11.12.2", width) + ansiReset,
	}, true
}

func renderMermaidRequirement(source string, width int, theme themePalette) ([]string, bool) {
	lines := strings.Split(source, "\n")
	statements := 0
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line != "" && !strings.HasPrefix(line, "%%") {
			statements++
			if statements > maxMermaidStatements {
				return nil, false
			}
		}
	}
	nodes := make([]mermaidRequirementNode, 0)
	nodeIndex := make(map[string]int)
	relations := make([]mermaidRequirementRelation, 0)
	direction := "TB"
	foundHeader := false
	for index := 0; index < len(lines); {
		line := strings.TrimSpace(lines[index])
		index++
		if line == "" || strings.HasPrefix(line, "%%") {
			continue
		}
		if !foundHeader {
			if strings.Fields(line)[0] != "requirementDiagram" {
				return nil, false
			}
			foundHeader = true
			continue
		}
		if value, ok := strings.CutPrefix(line, "direction "); ok {
			value = strings.ToUpper(strings.TrimSpace(value))
			switch value {
			case "TB", "TD":
				direction = "TB"
			case "BT", "LR", "RL":
				direction = value
			default:
				return nil, false
			}
			continue
		}
		if node, next, ok, valid := mermaidRequirementParseNode(lines, index-1); ok {
			if !valid {
				return nil, false
			}
			if existing, exists := nodeIndex[node.name]; exists {
				nodes[existing] = node
			} else {
				if len(nodes) == maxMermaidNodes {
					return nil, false
				}
				nodeIndex[node.name] = len(nodes)
				nodes = append(nodes, node)
			}
			index = next
			continue
		}
		if relation, ok := mermaidRequirementParseRelation(line); ok {
			if len(relations) == maxMermaidRelations {
				return nil, false
			}
			relations = append(relations, relation)
			continue
		}
		return nil, false
	}
	if !foundHeader {
		return nil, false
	}

	rendered := []string{
		ansiDim + mermaidFit("◇ mermaid requirement", width) + ansiReset,
		theme.heading + mermaidFit("direction: "+direction, width) + ansiReset,
	}
	for _, node := range nodes {
		rendered = append(rendered, theme.code+mermaidFit("<<"+node.kind+">> "+node.name, width)+ansiReset)
		for _, property := range node.body {
			rendered = append(rendered, ansiDim+mermaidFit("  "+property, width)+ansiReset)
		}
	}
	for _, relation := range relations {
		rendered = append(rendered, theme.code+mermaidFit(relation.from+" ─<<"+relation.kind+">>→ "+relation.to, width)+ansiReset)
	}
	return rendered, true
}

func renderMermaidC4(source string, width int, theme themePalette) ([]string, bool) {
	validHeaders := map[string]bool{
		"C4Context": true, "C4Container": true, "C4Component": true,
		"C4Dynamic": true, "C4Deployment": true,
	}
	shapeKinds := map[string]string{
		"Person": "person", "Person_Ext": "external_person",
		"System": "system", "System_Ext": "external_system",
		"SystemDb": "system_db", "SystemDb_Ext": "external_system_db",
		"SystemQueue": "system_queue", "SystemQueue_Ext": "external_system_queue",
		"Container": "container", "Container_Ext": "external_container",
		"ContainerDb": "container_db", "ContainerDb_Ext": "external_container_db",
		"ContainerQueue": "container_queue", "ContainerQueue_Ext": "external_container_queue",
		"Component": "component", "Component_Ext": "external_component",
		"ComponentDb": "component_db", "ComponentDb_Ext": "external_component_db",
		"ComponentQueue": "component_queue", "ComponentQueue_Ext": "external_component_queue",
	}
	boundaryKinds := map[string]bool{
		"Boundary": true, "Enterprise_Boundary": true, "System_Boundary": true,
		"Container_Boundary": true, "Deployment_Node": true,
		"Deployment_Node_L": true, "Deployment_Node_R": true,
	}
	relationKinds := map[string]bool{
		"Rel": true, "Rel_U": true, "Rel_D": true, "Rel_L": true,
		"Rel_R": true, "Rel_Back": true, "Rel_Neighbor": true,
	}
	biRelationKinds := map[string]bool{
		"BiRel": true, "BiRel_U": true, "BiRel_D": true,
		"BiRel_L": true, "BiRel_R": true, "BiRel_Neighbor": true,
	}
	lines := strings.Split(source, "\n")
	statements := 0
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line != "" && !strings.HasPrefix(line, "%%") {
			statements++
			if statements > maxMermaidStatements {
				return nil, false
			}
		}
	}

	header := ""
	title := ""
	shapes := make([]mermaidC4Shape, 0)
	boundaries := make([]mermaidC4Boundary, 0)
	relations := make([]mermaidC4Relation, 0)
	boundaryStack := []string{"global"}
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "%%") {
			continue
		}
		if header == "" {
			token := strings.Fields(line)[0]
			if !validHeaders[token] {
				return nil, false
			}
			header = token
			continue
		}
		if value, ok := strings.CutPrefix(line, "title "); ok {
			title = strings.TrimSpace(value)
			continue
		}
		if line == "title" {
			continue
		}
		if line == "}" || line == "end" {
			if len(boundaryStack) > 1 {
				boundaryStack = boundaryStack[:len(boundaryStack)-1]
			}
			continue
		}
		name, args, ok := mermaidC4Statement(line)
		if !ok {
			continue
		}
		if kind, ok := shapeKinds[name]; ok {
			if len(shapes)+len(boundaries) == maxMermaidNodes {
				return nil, false
			}
			shape := mermaidC4Shape{alias: mermaidC4Arg(args, 0), kind: kind, label: mermaidC4Arg(args, 1), boundary: boundaryStack[len(boundaryStack)-1]}
			if strings.HasPrefix(name, "Person") || strings.HasPrefix(name, "System") {
				shape.detail = mermaidC4Arg(args, 2)
			} else {
				shape.tech = mermaidC4Arg(args, 2)
				shape.detail = mermaidC4Arg(args, 3)
			}
			shapes = append(shapes, shape)
			continue
		}
		if boundaryKinds[name] {
			if len(shapes)+len(boundaries) == maxMermaidNodes {
				return nil, false
			}
			boundary := mermaidC4Boundary{alias: mermaidC4Arg(args, 0), label: mermaidC4Arg(args, 1), kind: mermaidC4Arg(args, 2)}
			boundaries = append(boundaries, boundary)
			boundaryStack = append(boundaryStack, boundary.alias)
			continue
		}
		if relationKinds[name] || biRelationKinds[name] {
			if len(relations) == maxMermaidRelations {
				return nil, false
			}
			relations = append(relations, mermaidC4Relation{
				from: mermaidC4Arg(args, 0), to: mermaidC4Arg(args, 1),
				label: mermaidC4Arg(args, 2), tech: mermaidC4Arg(args, 3),
			})
		}
	}
	if header == "" {
		return nil, false
	}

	boundaryByAlias := make(map[string]mermaidC4Boundary, len(boundaries))
	for _, boundary := range boundaries {
		boundaryByAlias[boundary.alias] = boundary
	}
	shapeAliases := make(map[string]bool, len(shapes))
	usedBoundaries := make(map[string]bool, len(boundaries))
	for _, shape := range shapes {
		shapeAliases[shape.alias] = true
		usedBoundaries[shape.boundary] = true
	}
	rendered := []string{ansiDim + mermaidFit("◇ mermaid C4", width) + ansiReset}
	rendered = append(rendered, theme.heading+mermaidFit(header, width)+ansiReset)
	if title != "" {
		rendered = append(rendered, theme.heading+mermaidFit(title, width)+ansiReset)
	}
	for _, boundary := range boundaries {
		if !usedBoundaries[boundary.alias] {
			continue
		}
		label := boundary.label
		if boundary.kind != "" {
			label += " [" + boundary.kind + "]"
		}
		rendered = append(rendered, theme.heading+mermaidFit("boundary: "+label, width)+ansiReset)
	}
	for _, shape := range shapes {
		line := "<<" + shape.kind + ">> " + shape.alias
		if shape.label != "" {
			line += ": " + shape.label
		}
		if shape.tech != "" {
			line += " [" + shape.tech + "]"
		}
		rendered = append(rendered, theme.code+mermaidFit(line, width)+ansiReset)
		if shape.detail != "" {
			rendered = append(rendered, ansiDim+mermaidFit("  "+shape.detail, width)+ansiReset)
		}
		if boundary, ok := boundaryByAlias[shape.boundary]; ok {
			rendered = append(rendered, ansiDim+mermaidFit("  in: "+boundary.label, width)+ansiReset)
		}
	}
	for index, relation := range relations {
		if !shapeAliases[relation.from] || !shapeAliases[relation.to] {
			continue
		}
		label := relation.label
		if header == "C4Dynamic" {
			label = strconv.Itoa(index+1) + ": " + label
		}
		line := relation.from + " → " + relation.to
		if label != "" {
			line += ": " + label
		}
		if relation.tech != "" {
			line += " [" + relation.tech + "]"
		}
		rendered = append(rendered, theme.code+mermaidFit(line, width)+ansiReset)
	}
	return rendered, true
}

func mermaidC4Statement(line string) (string, []string, bool) {
	open := strings.IndexByte(strings.TrimSpace(line), '(')
	line = strings.TrimSpace(line)
	if open <= 0 {
		return "", nil, false
	}
	name := strings.TrimSpace(line[:open])
	depth := 0
	quoted := false
	close := -1
	for offset, char := range line[open+1:] {
		switch char {
		case '"':
			quoted = !quoted
		case '(':
			if !quoted {
				depth++
			}
		case ')':
			if !quoted {
				if depth == 0 {
					close = open + 1 + offset
				} else {
					depth--
				}
			}
		}
		if close >= 0 {
			break
		}
	}
	if close < 0 {
		return "", nil, false
	}
	return name, mermaidC4Args(line[open+1 : close]), true
}

func mermaidC4Args(value string) []string {
	args := make([]string, 0)
	var current strings.Builder
	depth := 0
	quoted := false
	for _, char := range value {
		switch char {
		case '"':
			quoted = !quoted
		case '(':
			if !quoted {
				depth++
			}
			current.WriteRune(char)
		case ')':
			if !quoted {
				depth--
			}
			current.WriteRune(char)
		case ',':
			if !quoted && depth == 0 {
				args = append(args, strings.TrimSpace(current.String()))
				current.Reset()
			} else {
				current.WriteRune(char)
			}
		default:
			current.WriteRune(char)
		}
	}
	if last := strings.TrimSpace(current.String()); last != "" {
		args = append(args, last)
	}
	return args
}

func mermaidC4Arg(args []string, index int) string {
	if index >= len(args) {
		return ""
	}
	return args[index]
}

func mermaidRequirementParseNode(lines []string, start int) (mermaidRequirementNode, int, bool, bool) {
	fields := strings.Fields(strings.TrimSpace(lines[start]))
	if len(fields) < 2 {
		return mermaidRequirementNode{}, start + 1, false, true
	}
	kinds := map[string]string{
		"requirement":            "Requirement",
		"functionalrequirement":  "Functional Requirement",
		"interfacerequirement":   "Interface Requirement",
		"performancerequirement": "Performance Requirement",
		"physicalrequirement":    "Physical Requirement",
		"designconstraint":       "Design Constraint",
		"element":                "Element",
	}
	kind, ok := kinds[strings.ToLower(fields[0])]
	if !ok {
		return mermaidRequirementNode{}, start + 1, false, true
	}
	name := strings.TrimSpace(strings.TrimSuffix(fields[1], "{"))
	if name == "" {
		return mermaidRequirementNode{}, start + 1, true, false
	}
	hasBrace := strings.Contains(strings.Join(fields[2:], " "), "{") || strings.HasSuffix(fields[1], "{")
	index := start + 1
	if !hasBrace {
		for index < len(lines) {
			line := strings.TrimSpace(lines[index])
			if line == "" || strings.HasPrefix(line, "%%") {
				index++
				continue
			}
			if strings.HasPrefix(line, "{") {
				index++
				break
			}
			return mermaidRequirementNode{}, start + 1, false, true
		}
	}
	properties := make(map[string]string)
	for index < len(lines) {
		line := strings.TrimSpace(lines[index])
		index++
		if line == "" || strings.HasPrefix(line, "%%") {
			continue
		}
		if strings.HasPrefix(line, "}") {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return mermaidRequirementNode{}, index, true, false
		}
		properties[strings.TrimSpace(key)] = mermaidRequirementValue(value)
	}
	body := make([]string, 0, 4)
	if kind == "Element" {
		if value := properties["type"]; value != "" {
			body = append(body, "Type: "+value)
		}
		value, exists := properties["docref"]
		if !exists {
			value = properties["docRef"]
		}
		if value != "" {
			body = append(body, "Doc Ref: "+value)
		}
	} else {
		if value := properties["id"]; value != "" {
			body = append(body, "ID: "+value)
		}
		if value := properties["text"]; value != "" {
			body = append(body, "Text: "+value)
		}
		if value := properties["risk"]; value != "" {
			body = append(body, "Risk: "+mermaidRequirementTitle(value, "low", "medium", "high"))
		}
		value, exists := properties["verifyMethod"]
		if !exists {
			value = properties["verifymethod"]
		}
		if value != "" {
			body = append(body, "Verification: "+mermaidRequirementTitle(value, "analysis", "demonstration", "inspection", "test"))
		}
	}
	return mermaidRequirementNode{name: name, kind: kind, body: body}, index, true, true
}

func mermaidRequirementValue(value string) string {
	value = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), ","))
	if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
		return value[1 : len(value)-1]
	}
	return value
}

func mermaidRequirementTitle(value string, known ...string) string {
	trimmed := strings.TrimSpace(value)
	for _, candidate := range known {
		if strings.EqualFold(trimmed, candidate) {
			return strings.ToUpper(candidate[:1]) + candidate[1:]
		}
	}
	return trimmed
}

func mermaidRequirementParseRelation(line string) (mermaidRequirementRelation, bool) {
	if left, right, ok := strings.Cut(line, "->"); ok {
		tokens := mermaidRequirementRelationTokens(left)
		if len(tokens) < 2 || strings.TrimSpace(right) == "" {
			return mermaidRequirementRelation{}, false
		}
		return mermaidRequirementRelation{from: tokens[0], to: strings.TrimSpace(right), kind: tokens[1]}, true
	}
	if left, right, ok := strings.Cut(line, "<-"); ok {
		tokens := mermaidRequirementRelationTokens(right)
		if len(tokens) < 2 || strings.TrimSpace(left) == "" {
			return mermaidRequirementRelation{}, false
		}
		return mermaidRequirementRelation{from: tokens[1], to: strings.TrimSpace(left), kind: tokens[0]}, true
	}
	return mermaidRequirementRelation{}, false
}

func mermaidRequirementRelationTokens(value string) []string {
	tokens := strings.Fields(value)
	filtered := tokens[:0]
	for _, token := range tokens {
		if token != "-" {
			filtered = append(filtered, token)
		}
	}
	return filtered
}

func renderMermaidKanban(source string, width int, theme themePalette) ([]string, bool) {
	columns := make([]mermaidKanbanColumn, 0)
	foundHeader := false
	statements := 0
	for _, raw := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		statements++
		if statements > maxMermaidStatements {
			return nil, false
		}
		if !foundHeader {
			if strings.Fields(trimmed)[0] != "kanban" {
				return nil, false
			}
			foundHeader = true
			continue
		}
		first, _ := utf8.DecodeRuneInString(raw)
		if !unicode.IsSpace(first) {
			columns = append(columns, mermaidKanbanColumn{title: trimmed})
			continue
		}
		if len(columns) == 0 {
			return nil, false
		}
		task, ok := mermaidKanbanParseTask(trimmed)
		if !ok {
			return nil, false
		}
		last := len(columns) - 1
		columns[last].tasks = append(columns[last].tasks, task)
	}
	if !foundHeader {
		return nil, false
	}

	lines := []string{ansiDim + mermaidFit("◇ mermaid kanban", width) + ansiReset}
	for _, column := range columns {
		lines = append(lines, theme.heading+mermaidFit(column.title, width)+ansiReset)
		for index, task := range column.tasks {
			connector := "├─ "
			if index == len(column.tasks)-1 {
				connector = "└─ "
			}
			lines = append(lines, theme.code+mermaidFit(connector+task.label, width)+ansiReset)
			metadata := make([]string, 0, 3)
			if task.assigned != nil {
				metadata = append(metadata, "assigned: "+*task.assigned)
			}
			if task.priority != nil {
				metadata = append(metadata, "priority: "+*task.priority)
			}
			if task.ticket != nil {
				metadata = append(metadata, "ticket: "+*task.ticket)
			}
			if len(metadata) > 0 {
				lines = append(lines, ansiDim+mermaidFit("   "+strings.Join(metadata, " · "), width)+ansiReset)
			}
		}
	}
	return lines, true
}

func mermaidKanbanParseTask(line string) (mermaidKanbanTask, bool) {
	task := mermaidKanbanTask{label: line}
	start := strings.Index(line, "@{")
	if start < 0 || !strings.HasSuffix(line, "}") {
		return task, true
	}
	task.label = strings.TrimRightFunc(line[:start], unicode.IsSpace)
	metadata := strings.TrimSpace(line[start+2 : len(line)-1])
	yamlText := "{\n" + metadata + "\n}"
	var document yaml.Node
	if yaml.Unmarshal([]byte(yamlText), &document) != nil || len(document.Content) != 1 {
		return mermaidKanbanTask{}, false
	}
	mapping := document.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return mermaidKanbanTask{}, false
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		key, value := mapping.Content[index], mapping.Content[index+1]
		if key.Kind != yaml.ScalarNode {
			continue
		}
		text, ok := mermaidKanbanScalar(value)
		if !ok {
			continue
		}
		switch key.Value {
		case "label":
			task.label = text
		case "assigned":
			task.assigned = &text
		case "priority":
			task.priority = &text
		case "ticket":
			task.ticket = &text
		}
	}
	return task, true
}

func mermaidKanbanScalar(node *yaml.Node) (string, bool) {
	if node.Kind != yaml.ScalarNode {
		return "", false
	}
	switch node.Tag {
	case "!!str", "!!int", "!!float":
		return node.Value, true
	case "!!bool":
		value, err := strconv.ParseBool(node.Value)
		if err != nil {
			return "", false
		}
		return strconv.FormatBool(value), true
	default:
		return "", false
	}
}

func renderMermaidBlock(source string, width int, theme themePalette) ([]string, bool) {
	nodes := make(map[string]string)
	order := make([]string, 0)
	edges := make([]mermaidBlockEdge, 0)
	columns := -1
	foundHeader := false
	statements := 0
	insertNode := func(id, label string) {
		if _, exists := nodes[id]; !exists {
			order = append(order, id)
			nodes[id] = label
		} else if label != id {
			nodes[id] = label
		}
	}
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
			if strings.Fields(statement)[0] != "block-beta" {
				return nil, false
			}
			foundHeader = true
			continue
		}
		if value, ok := strings.CutPrefix(statement, "columns"); ok {
			value = strings.TrimSpace(value)
			if value == "auto" {
				columns = -1
			} else if parsed, err := strconv.Atoi(value); err == nil {
				columns = parsed
			}
			continue
		}
		if statement == "end" || strings.HasPrefix(statement, "block:") || strings.HasPrefix(statement, "block ") ||
			strings.HasPrefix(statement, "style ") || strings.HasPrefix(statement, "classDef ") ||
			strings.HasPrefix(statement, "class ") || strings.HasPrefix(statement, "linkStyle ") ||
			statement == "space" || strings.HasPrefix(statement, "space:") {
			continue
		}
		if left, right, ok := strings.Cut(statement, "-->"); ok {
			from, fromLabel, valid := mermaidBlockNode(left)
			if !valid {
				return nil, false
			}
			to, toLabel, valid := mermaidBlockNode(right)
			if !valid {
				return nil, false
			}
			insertNode(from, fromLabel)
			insertNode(to, toLabel)
			edges = append(edges, mermaidBlockEdge{from: from, to: to})
			continue
		}
		if id, label, valid := mermaidBlockNode(statement); valid {
			insertNode(id, label)
		}
	}
	lines := []string{ansiDim + mermaidFit("◇ mermaid block", width) + ansiReset}
	columnText := "auto"
	if columns >= 0 {
		columnText = strconv.Itoa(columns)
	}
	lines = append(lines, theme.heading+mermaidFit("columns: "+columnText, width)+ansiReset)
	rowSize := len(order)
	if columns > 0 && columns < rowSize {
		rowSize = columns
	}
	if rowSize > 0 {
		for start := 0; start < len(order); start += rowSize {
			end := min(start+rowSize, len(order))
			labels := make([]string, 0, end-start)
			for _, id := range order[start:end] {
				labels = append(labels, "["+nodes[id]+"]")
			}
			lines = append(lines, theme.code+mermaidFit(strings.Join(labels, " │ "), width)+ansiReset)
		}
	}
	for _, edge := range edges {
		lines = append(lines, theme.code+mermaidFit(nodes[edge.from]+" → "+nodes[edge.to], width)+ansiReset)
	}
	return lines, true
}

func mermaidBlockNode(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", false
	}
	if start := strings.IndexByte(value, '['); start >= 0 {
		id := strings.TrimSpace(value[:start])
		if id == "" {
			return "", "", false
		}
		label := strings.TrimSpace(value[start+1:])
		label = strings.TrimSpace(strings.TrimSuffix(label, "]"))
		return id, mermaidUnquote(label), true
	}
	return value, value, true
}

func renderMermaidMindmap(source string, width int, theme themePalette) ([]string, bool) {
	type stackEntry struct {
		indent int
		node   *mermaidMindmapNode
	}
	stack := make([]stackEntry, 0)
	foundHeader := false
	statements := 0
	for _, raw := range strings.Split(source, "\n") {
		text := strings.TrimSpace(raw)
		if text == "" || strings.HasPrefix(strings.TrimLeft(raw, " \t"), "%%") {
			continue
		}
		statements++
		if statements > maxMermaidStatements {
			return nil, false
		}
		if !foundHeader {
			if strings.Fields(text)[0] != "mindmap" {
				return nil, false
			}
			foundHeader = true
			continue
		}
		if strings.HasPrefix(text, "::") {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
		isRoot := len(stack) == 0
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			child := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				stack = append(stack, stackEntry{indent: indent, node: child.node})
				break
			}
			parent := stack[len(stack)-1].node
			parent.children = append(parent.children, child.node)
		}
		label, shape := mermaidMindmapLabel(text)
		if isRoot && shape == "•" {
			shape = "●"
		}
		stack = append(stack, stackEntry{indent: indent, node: &mermaidMindmapNode{label: label, shape: shape}})
	}
	for len(stack) > 1 {
		child := stack[len(stack)-1].node
		stack = stack[:len(stack)-1]
		parent := stack[len(stack)-1].node
		parent.children = append(parent.children, child)
	}
	if len(stack) == 0 {
		return nil, false
	}
	lines := []string{ansiDim + mermaidFit("◇ mermaid mindmap", width) + ansiReset}
	root := stack[0].node
	lines = append(lines, theme.heading+mermaidFit(root.shape+" "+root.label, width)+ansiReset)
	var renderChildren func(*mermaidMindmapNode, string)
	renderChildren = func(parent *mermaidMindmapNode, prefix string) {
		for index, child := range parent.children {
			last := index == len(parent.children)-1
			connector := "├─"
			nextPrefix := prefix + "│ "
			if last {
				connector = "└─"
				nextPrefix = prefix + "  "
			}
			lines = append(lines, theme.code+mermaidFit(prefix+connector+child.shape+" "+child.label, width)+ansiReset)
			renderChildren(child, nextPrefix)
		}
	}
	renderChildren(root, "")
	return lines, true
}

func mermaidMindmapLabel(text string) (string, string) {
	text = strings.TrimSpace(text)
	for _, shape := range []struct {
		open   string
		close  string
		symbol string
	}{
		{open: "((", close: "))", symbol: "○"},
		{open: "{{", close: "}}", symbol: "⬡"},
		{open: "))", close: "((", symbol: "✦"},
		{open: "[", close: "]", symbol: "□"},
		{open: "(", close: ")", symbol: "▢"},
	} {
		start := strings.Index(text, shape.open)
		if start >= 0 && strings.HasSuffix(text, shape.close) && start+len(shape.open) < len(text)-len(shape.close) {
			return strings.TrimSpace(text[start+len(shape.open) : len(text)-len(shape.close)]), shape.symbol
		}
	}
	return text, "•"
}

func renderMermaidXYChart(source string, width int, theme themePalette) ([]string, bool) {
	title := ""
	xAxis := mermaidXYAxis{numeric: true}
	yAxis := mermaidXYAxis{numeric: true}
	hasYRange := false
	series := make([][]float64, 0)
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
			if strings.Fields(statement)[0] != "xychart-beta" {
				return nil, false
			}
			foundHeader = true
			continue
		}
		if value, ok := strings.CutPrefix(statement, "title "); ok {
			title = mermaidUnquote(value)
			continue
		}
		if value, ok := strings.CutPrefix(statement, "x-axis "); ok {
			parsed, valid := mermaidXYChartXAxis(value)
			if !valid {
				return nil, false
			}
			xAxis = parsed
			continue
		}
		if value, ok := strings.CutPrefix(statement, "y-axis "); ok {
			axis, ranged, valid := mermaidXYChartRange(value)
			if !valid {
				return nil, false
			}
			yAxis.title = axis.title
			if ranged {
				yAxis.min, yAxis.max = axis.min, axis.max
				hasYRange = true
			}
			continue
		}
		if value, ok := mermaidXYChartLine(statement); ok {
			values, valid := mermaidXYChartNumbers(value)
			if !valid {
				return nil, false
			}
			series = append(series, values)
		}
	}
	hasValues := false
	for _, values := range series {
		if len(values) > 0 {
			hasValues = true
			break
		}
	}
	if !hasValues {
		return nil, false
	}
	if !hasYRange {
		yAxis.min, yAxis.max = mermaidXYChartAutoRange(series)
	}
	lines := []string{ansiDim + mermaidFit("◇ mermaid xychart", width) + ansiReset}
	if title != "" {
		lines = append(lines, theme.heading+mermaidFit(title, width)+ansiReset)
	}
	xLabel := xAxis.title
	if xAxis.numeric {
		xLabel = strings.TrimSpace(xLabel + " " + mermaidXYChartRangeText(xAxis.min, xAxis.max))
	} else if len(xAxis.categories) > 0 {
		xLabel = strings.TrimSpace(xLabel + " [" + strings.Join(xAxis.categories, " · ") + "]")
	}
	lines = append(lines, theme.code+mermaidFit("x: "+xLabel, width)+ansiReset)
	yLabel := strings.TrimSpace(yAxis.title + " " + mermaidXYChartRangeText(yAxis.min, yAxis.max))
	lines = append(lines, theme.code+mermaidFit("y: "+yLabel, width)+ansiReset)
	for index, values := range series {
		if len(values) == 0 {
			continue
		}
		parts := make([]string, len(values))
		for valueIndex, value := range values {
			parts[valueIndex] = strconv.FormatFloat(value, 'g', -1, 64)
		}
		lines = append(lines, theme.code+mermaidFit("line "+strconv.Itoa(index+1)+": "+strings.Join(parts, " · "), width)+ansiReset)
	}
	return lines, true
}

func mermaidXYChartXAxis(value string) (mermaidXYAxis, bool) {
	value = strings.TrimSpace(value)
	if open := strings.IndexByte(value, '['); open >= 0 {
		close := strings.LastIndexByte(value, ']')
		if close <= open {
			return mermaidXYAxis{}, false
		}
		return mermaidXYAxis{title: mermaidUnquote(value[:open]), categories: mermaidXYChartCategories(value[open+1 : close])}, true
	}
	if strings.Contains(value, "-->") {
		axis, _, valid := mermaidXYChartRange(value)
		return axis, valid
	}
	return mermaidXYAxis{title: mermaidUnquote(value)}, true
}

func mermaidXYChartRange(value string) (mermaidXYAxis, bool, bool) {
	value = strings.TrimSpace(value)
	left, right, ranged := strings.Cut(value, "-->")
	if !ranged {
		return mermaidXYAxis{title: mermaidUnquote(value), numeric: true}, false, true
	}
	maximum, err := strconv.ParseFloat(strings.TrimSpace(right), 64)
	if err != nil {
		return mermaidXYAxis{}, false, false
	}
	left = strings.TrimSpace(left)
	split := strings.LastIndexFunc(left, unicode.IsSpace)
	title, minimumText := "", left
	if split >= 0 {
		title, minimumText = strings.TrimSpace(left[:split]), strings.TrimSpace(left[split:])
	}
	minimum, err := strconv.ParseFloat(minimumText, 64)
	if err != nil {
		return mermaidXYAxis{}, false, false
	}
	return mermaidXYAxis{title: mermaidUnquote(title), min: minimum, max: maximum, numeric: true}, true, true
}

func mermaidXYChartLine(statement string) (string, bool) {
	value, ok := strings.CutPrefix(statement, "line")
	first, _ := utf8.DecodeRuneInString(value)
	if !ok || value != "" && !unicode.IsSpace(first) && first != '[' {
		return "", false
	}
	return strings.TrimSpace(value), true
}

func mermaidXYChartNumbers(value string) ([]float64, bool) {
	start, end := strings.IndexByte(value, '['), strings.LastIndexByte(value, ']')
	if start < 0 || end <= start {
		return nil, false
	}
	values := make([]float64, 0)
	for _, part := range strings.Split(value[start+1:end], ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		parsed, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return nil, false
		}
		values = append(values, parsed)
	}
	return values, true
}

func mermaidXYChartCategories(value string) []string {
	parts := make([]string, 0)
	start := 0
	quote := byte(0)
	for index := 0; index < len(value); index++ {
		switch value[index] {
		case '\'', '"':
			if quote == 0 {
				quote = value[index]
			} else if quote == value[index] {
				quote = 0
			}
		case ',':
			if quote == 0 {
				if item := mermaidUnquote(value[start:index]); item != "" {
					parts = append(parts, item)
				}
				start = index + 1
			}
		}
	}
	if item := mermaidUnquote(value[start:]); item != "" {
		parts = append(parts, item)
	}
	return parts
}

func mermaidUnquote(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == value[len(value)-1] && (value[0] == '\'' || value[0] == '"') {
		return value[1 : len(value)-1]
	}
	return value
}

func mermaidXYChartAutoRange(series [][]float64) (float64, float64) {
	minimum, maximum := math.Inf(1), math.Inf(-1)
	for _, values := range series {
		for _, value := range values {
			if !math.IsNaN(value) {
				minimum, maximum = math.Min(minimum, value), math.Max(maximum, value)
			}
		}
	}
	if math.IsInf(minimum, 0) || math.IsInf(maximum, 0) || math.IsNaN(minimum) || math.IsNaN(maximum) {
		return 0, 0
	}
	if math.Abs(maximum-minimum) < math.Nextafter(1, 2)-1 {
		return minimum - 1, maximum + 1
	}
	return minimum, maximum
}

func mermaidXYChartRangeText(minimum, maximum float64) string {
	return strconv.FormatFloat(minimum, 'g', -1, 64) + " → " + strconv.FormatFloat(maximum, 'g', -1, 64)
}

func renderMermaidRadar(source string, width int, theme themePalette) ([]string, bool) {
	axes := []string(nil)
	curves := make([]mermaidRadarCurve, 0)
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
			if strings.Fields(statement)[0] != "radar-beta" {
				return nil, false
			}
			foundHeader = true
			continue
		}
		if value, ok := strings.CutPrefix(statement, "axis "); ok {
			axes = axes[:0]
			for _, axis := range strings.Split(value, ",") {
				if axis = strings.TrimSpace(axis); axis != "" {
					axes = append(axes, axis)
				}
			}
			continue
		}
		if value, ok := strings.CutPrefix(statement, "curve "); ok {
			name, rawValues, valid := strings.Cut(strings.TrimSpace(value), "{")
			if !valid || !strings.HasSuffix(rawValues, "}") {
				return nil, false
			}
			values := make([]float64, 0)
			for _, rawValue := range strings.Split(strings.TrimSpace(strings.TrimSuffix(rawValues, "}")), ",") {
				rawValue = strings.TrimSpace(rawValue)
				if rawValue == "" {
					continue
				}
				parsed, err := strconv.ParseFloat(rawValue, 64)
				if err != nil {
					return nil, false
				}
				values = append(values, parsed)
			}
			curves = append(curves, mermaidRadarCurve{name: strings.TrimSpace(name), values: values})
		}
	}
	if len(axes) == 0 {
		return nil, false
	}
	lines := []string{ansiDim + mermaidFit("◇ mermaid radar", width) + ansiReset}
	lines = append(lines, theme.heading+mermaidFit("axes: "+strings.Join(axes, " · "), width)+ansiReset)
	for _, curve := range curves {
		if len(curve.values) != len(axes) {
			continue
		}
		parts := make([]string, len(axes))
		for index, axis := range axes {
			parts[index] = axis + "=" + strconv.FormatFloat(curve.values[index], 'g', -1, 64)
		}
		lines = append(lines, theme.code+mermaidFit(curve.name+": "+strings.Join(parts, " · "), width)+ansiReset)
	}
	return lines, true
}

func renderMermaidQuadrant(source string, width int, theme themePalette) ([]string, bool) {
	title := ""
	axes := make(map[string][2]string)
	quadrants := make(map[int]string)
	points := make([]mermaidQuadrantPoint, 0)
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
			if strings.Fields(statement)[0] != "quadrantChart" {
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
		if axis, value, ok := mermaidQuadrantAxis(statement); ok {
			low, high, valid := strings.Cut(value, "-->")
			if !valid {
				return nil, false
			}
			axes[axis] = [2]string{strings.TrimSpace(low), strings.TrimSpace(high)}
			continue
		}
		if value, ok := strings.CutPrefix(statement, "quadrant-"); ok {
			number, label, valid := strings.Cut(value, " ")
			index, err := strconv.Atoi(strings.TrimSpace(number))
			if !valid || err != nil {
				return nil, false
			}
			quadrants[index] = strings.TrimSpace(label)
			continue
		}
		label, coordinates, ok := strings.Cut(statement, ":")
		coordinates = strings.TrimSpace(coordinates)
		if !ok || len(coordinates) < 2 || coordinates[0] != '[' || coordinates[len(coordinates)-1] != ']' {
			return nil, false
		}
		parts := strings.Split(coordinates[1:len(coordinates)-1], ",")
		if len(parts) != 2 {
			return nil, false
		}
		x, xErr := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		y, yErr := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if xErr != nil || yErr != nil {
			return nil, false
		}
		label = strings.TrimSpace(label)
		if len(label) >= 2 && label[0] == '"' && label[len(label)-1] == '"' {
			label = label[1 : len(label)-1]
		}
		points = append(points, mermaidQuadrantPoint{label: label, x: x, y: y})
	}
	if len(points) == 0 {
		return nil, false
	}
	lines := []string{ansiDim + mermaidFit("◇ mermaid quadrant", width) + ansiReset}
	if title != "" {
		lines = append(lines, theme.heading+mermaidFit(title, width)+ansiReset)
	}
	for _, axis := range []string{"x", "y"} {
		if labels, ok := axes[axis]; ok {
			lines = append(lines, theme.code+mermaidFit(axis+": "+labels[0]+" → "+labels[1], width)+ansiReset)
		}
	}
	for index := 1; index <= 4; index++ {
		if label, ok := quadrants[index]; ok {
			lines = append(lines, theme.heading+mermaidFit("Q"+strconv.Itoa(index)+": "+label, width)+ansiReset)
		}
	}
	for _, point := range points {
		quadrant := mermaidPointQuadrant(point.x, point.y)
		text := quadrant + " • " + point.label + " [" + strconv.FormatFloat(point.x, 'g', -1, 64) + ", " + strconv.FormatFloat(point.y, 'g', -1, 64) + "]"
		lines = append(lines, theme.code+mermaidFit(text, width)+ansiReset)
	}
	return lines, true
}

func mermaidQuadrantAxis(statement string) (string, string, bool) {
	for _, axis := range []string{"x", "y"} {
		if value, ok := strings.CutPrefix(statement, axis+"-axis "); ok {
			return axis, strings.TrimSpace(value), true
		}
	}
	return "", "", false
}

func mermaidPointQuadrant(x, y float64) string {
	x = math.Max(0, math.Min(1, x))
	y = math.Max(0, math.Min(1, y))
	switch {
	case math.IsNaN(x) || math.IsNaN(y):
		return "Q?"
	case x >= .5 && y >= .5:
		return "Q1"
	case x < .5 && y >= .5:
		return "Q2"
	case x < .5 && y < .5:
		return "Q3"
	default:
		return "Q4"
	}
}

func renderMermaidSankey(source string, width int, theme themePalette) ([]string, bool) {
	links := make([]mermaidSankeyLink, 0)
	nodes := make([]string, 0)
	seen := make(map[string]bool)
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
			if strings.Fields(statement)[0] != "sankey-beta" {
				return nil, false
			}
			foundHeader = true
			continue
		}
		parts := strings.Split(statement, ",")
		if len(parts) != 3 {
			return nil, false
		}
		sourceNode := strings.TrimSpace(parts[0])
		targetNode := strings.TrimSpace(parts[1])
		value, err := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
		if err != nil {
			return nil, false
		}
		for _, node := range []string{sourceNode, targetNode} {
			if !seen[node] {
				seen[node] = true
				nodes = append(nodes, node)
			}
		}
		links = append(links, mermaidSankeyLink{source: sourceNode, target: targetNode, value: value})
	}
	if len(links) == 0 {
		return nil, false
	}
	lines := []string{ansiDim + mermaidFit("◇ mermaid sankey", width) + ansiReset}
	lines = append(lines, theme.heading+mermaidFit("nodes: "+strings.Join(nodes, " · "), width)+ansiReset)
	for _, link := range links {
		value := strconv.FormatFloat(link.value, 'g', -1, 64)
		lines = append(lines, theme.code+mermaidFit(link.source+" ─"+value+"▶ "+link.target, width)+ansiReset)
	}
	return lines, true
}

func renderMermaidGitGraph(source string, width int, theme themePalette) ([]string, bool) {
	branches := map[string]int{"main": -1}
	branchOrder := []string{"main"}
	current := "main"
	head := -1
	commits := make([]mermaidGitCommit, 0)
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
		fields := strings.Fields(statement)
		if !foundHeader {
			if fields[0] != "gitGraph" {
				return nil, false
			}
			foundHeader = true
			continue
		}
		switch fields[0] {
		case "commit":
			parents := []int(nil)
			if head >= 0 {
				parents = []int{head}
			}
			commits = append(commits, mermaidGitCommit{branch: current, parents: parents})
			head = len(commits) - 1
			branches[current] = head
		case "branch":
			if len(fields) < 2 {
				return nil, false
			}
			name := fields[1]
			if _, exists := branches[name]; !exists {
				branches[name] = head
				branchOrder = append(branchOrder, name)
			}
		case "checkout":
			if len(fields) < 2 {
				return nil, false
			}
			name := fields[1]
			current = name
			var exists bool
			head, exists = branches[name]
			if !exists {
				head = -1
				branches[name] = head
				branchOrder = append(branchOrder, name)
			}
		case "merge":
			if len(fields) < 2 {
				return nil, false
			}
			parents := make([]int, 0, 2)
			if head >= 0 {
				parents = append(parents, head)
			}
			if otherHead, exists := branches[fields[1]]; exists && otherHead >= 0 {
				parents = append(parents, otherHead)
			}
			commits = append(commits, mermaidGitCommit{branch: current, parents: parents, merge: true})
			head = len(commits) - 1
			branches[current] = head
		default:
			return nil, false
		}
	}
	if !foundHeader {
		return nil, false
	}
	lines := []string{ansiDim + mermaidFit("◇ mermaid gitGraph", width) + ansiReset}
	lines = append(lines, theme.heading+mermaidFit("branches: "+strings.Join(branchOrder, " · "), width)+ansiReset)
	for index, commit := range commits {
		marker := "●"
		if commit.merge {
			marker = "◎"
		}
		text := marker + " " + strconv.Itoa(index) + " " + commit.branch
		if len(commit.parents) > 0 {
			parents := make([]string, len(commit.parents))
			for parentIndex, parent := range commit.parents {
				parents[parentIndex] = strconv.Itoa(parent)
			}
			text += " ← " + strings.Join(parents, ", ")
		}
		lines = append(lines, theme.code+mermaidFit(text, width)+ansiReset)
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
