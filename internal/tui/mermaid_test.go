package tui

import (
	"strconv"
	"strings"
	"testing"
)

func TestRenderMarkdownRendersClosedMermaidFlowchart(t *testing.T) {
	source := "Before\n\n```mermaid\nflowchart TD\n  A[\"Start; retry %% later --> here\"] --> B{Ready?} --> C[Ship]\n  B -->|no| D[Debug]\n  D --> B\n```\n\nAfter"
	lines := renderMarkdown(source, 48)
	rendered := stripUIANSI(strings.Join(lines, "\n"))
	for _, expected := range []string{"Before", "◇ mermaid", "Start; retry", "Ready?", "Ship", "Debug", "no", "After"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered Mermaid missing %q:\n%s", expected, rendered)
		}
	}
	if strings.Contains(rendered, "flowchart TD") || strings.Contains(rendered, `A["Start; retry %% later --> here"]`) {
		t.Fatalf("closed Mermaid source was not replaced:\n%s", rendered)
	}
	for _, line := range lines {
		if displayWidth(stripUIANSI(line)) > 48 {
			t.Fatalf("line exceeds width: %q", stripUIANSI(line))
		}
	}
}

func TestRenderMermaidSupportsReferenceDiagramKinds(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   []string
	}{
		{
			name: "sequence",
			source: "sequenceDiagram\nparticipant C as Client\nparticipant S as Server\n" +
				"C->>S: GET /items\nS-->>C: 200 OK\nNote over C,S: cached",
			want: []string{"Client", "Server", "GET /items", "200 OK", "cached"},
		},
		{
			name:   "state",
			source: "stateDiagram-v2\n[*] --> Idle\nIdle --> Ready: start\nReady --> [*]",
			want:   []string{"●", "Idle", "Ready", "start"},
		},
		{
			name: "class",
			source: "classDiagram\nclass Animal {\n+int age\n+mate()\n}\n" +
				"Animal <|-- Duck\nDuck *-- Bill",
			want: []string{"Animal", "Duck", "Bill", "◁", "◆"},
		},
		{
			name: "er",
			source: "erDiagram\nCUSTOMER {\nstring name\n}\nORDER {\nint number\n}\n" +
				"CUSTOMER ||--o{ ORDER : places",
			want: []string{"CUSTOMER", "ORDER", "places"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lines, ok := renderMermaid(test.source, 60, paletteFor("groknight"))
			if !ok {
				t.Fatal("supported Mermaid diagram was rejected")
			}
			rendered := stripUIANSI(strings.Join(lines, "\n"))
			for _, expected := range test.want {
				if !strings.Contains(rendered, expected) {
					t.Fatalf("rendered diagram missing %q:\n%s", expected, rendered)
				}
			}
		})
	}
}

func TestRenderMermaidPie(t *testing.T) {
	lines, ok := renderMermaid("pie showData\ntitle Work mix\n\"Build\" : 3\nTest : 1", 24, paletteFor("groknight"))
	if !ok {
		t.Fatal("supported Mermaid pie was rejected")
	}
	rendered := stripUIANSI(strings.Join(lines, "\n"))
	for _, expected := range []string{"◇ mermaid pie", "Work mix", "Build [3]", "75%", "Test [1]", "25%", "█"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered pie missing %q:\n%s", expected, rendered)
		}
	}
	if strings.Index(rendered, "Build") > strings.Index(rendered, "Test") {
		t.Fatalf("pie slices were not sorted by value:\n%s", rendered)
	}
	for _, line := range lines {
		if displayWidth(stripUIANSI(line)) > 24 {
			t.Fatalf("pie line exceeds width: %q", stripUIANSI(line))
		}
	}
}

func TestRenderMermaidPacket(t *testing.T) {
	source := "packet-beta\ntitle IPv4 header\n0-3: Version\n4-7: IHL\n8-31: Payload size\n32-39: 'Next row'"
	lines, ok := renderMermaid(source, 28, paletteFor("groknight"))
	if !ok {
		t.Fatal("supported Mermaid packet was rejected")
	}
	rendered := stripUIANSI(strings.Join(lines, "\n"))
	for _, expected := range []string{"◇ mermaid packet", "IPv4 header", "0-3 │ Version", "4-7 │ IHL", "8-31 │ Payload size", "32-39 │ Next row"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered packet missing %q:\n%s", expected, rendered)
		}
	}
	for _, line := range lines {
		if displayWidth(stripUIANSI(line)) > 28 {
			t.Fatalf("packet line exceeds width: %q", stripUIANSI(line))
		}
	}
}

func TestRenderMermaidGitGraph(t *testing.T) {
	source := "gitGraph\ncommit\ncommit\nbranch develop\ncheckout develop\ncommit\ncheckout main\nmerge develop"
	lines, ok := renderMermaid(source, 36, paletteFor("groknight"))
	if !ok {
		t.Fatal("supported Mermaid gitGraph was rejected")
	}
	rendered := stripUIANSI(strings.Join(lines, "\n"))
	for _, expected := range []string{
		"◇ mermaid gitGraph", "branches: main · develop",
		"● 0 main", "● 1 main ← 0", "● 2 develop ← 1", "◎ 3 main ← 1, 2",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered gitGraph missing %q:\n%s", expected, rendered)
		}
	}
	for _, line := range lines {
		if displayWidth(stripUIANSI(line)) > 36 {
			t.Fatalf("gitGraph line exceeds width: %q", stripUIANSI(line))
		}
	}
}

func TestRenderMermaidSankey(t *testing.T) {
	source := "sankey-beta\nA,B,10\nB,C,5\nB,D,5"
	lines, ok := renderMermaid(source, 28, paletteFor("groknight"))
	if !ok {
		t.Fatal("supported Mermaid sankey was rejected")
	}
	rendered := stripUIANSI(strings.Join(lines, "\n"))
	for _, expected := range []string{
		"◇ mermaid sankey", "nodes: A · B · C · D",
		"A ─10▶ B", "B ─5▶ C", "B ─5▶ D",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered sankey missing %q:\n%s", expected, rendered)
		}
	}
	for _, line := range lines {
		if displayWidth(stripUIANSI(line)) > 28 {
			t.Fatalf("sankey line exceeds width: %q", stripUIANSI(line))
		}
	}
}

func TestRenderMermaidQuadrant(t *testing.T) {
	source := "quadrantChart\ntitle Campaign reach\nx-axis Low reach --> High reach\ny-axis Low engagement --> High engagement\nquadrant-1 Expand\nquadrant-2 Promote\nquadrant-3 Rework\nquadrant-4 Improve\nAlpha: [0.8, 0.9]\nBeta: [0.2, 0.7]\nGamma: [0.1, 0.1]\nDelta: [0.9, 0.2]"
	lines, ok := renderMermaid(source, 48, paletteFor("groknight"))
	if !ok {
		t.Fatal("supported Mermaid quadrantChart was rejected")
	}
	rendered := stripUIANSI(strings.Join(lines, "\n"))
	for _, expected := range []string{
		"◇ mermaid quadrant", "Campaign reach",
		"x: Low reach → High reach", "y: Low engagement → High engagement",
		"Q1: Expand", "Q2: Promote", "Q3: Rework", "Q4: Improve",
		"Q1 • Alpha [0.8, 0.9]", "Q2 • Beta [0.2, 0.7]",
		"Q3 • Gamma [0.1, 0.1]", "Q4 • Delta [0.9, 0.2]",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered quadrantChart missing %q:\n%s", expected, rendered)
		}
	}
	for _, line := range lines {
		if displayWidth(stripUIANSI(line)) > 48 {
			t.Fatalf("quadrantChart line exceeds width: %q", stripUIANSI(line))
		}
	}
}

func TestRenderMermaidQuadrantMatchesReferenceParsing(t *testing.T) {
	source := "%% comment\nquadrantChart extra\ntitle First\ntitle Final\nx-axis left --> right --> extra\nquadrant-1 Old\nquadrant-1 New\nquadrant-5 Hidden\n\"Double quoted\": [-2, 3]\n'Single quoted': [0.5, 0.5]\n: [NaN, 0.2]"
	lines, ok := renderMermaid(source, 56, paletteFor("groknight"))
	if !ok {
		t.Fatal("reference-compatible quadrantChart was rejected")
	}
	rendered := stripUIANSI(strings.Join(lines, "\n"))
	for _, expected := range []string{
		"Final", "x: left → right --> extra", "Q1: New",
		"Q2 • Double quoted [-2, 3]", "Q1 • 'Single quoted' [0.5, 0.5]", "Q? •  [NaN, 0.2]",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("quadrantChart parsing differs from reference, missing %q:\n%s", expected, rendered)
		}
	}
	for _, unexpected := range []string{"First", "Q1: Old", "Hidden"} {
		if strings.Contains(rendered, unexpected) {
			t.Fatalf("quadrantChart retained overwritten or non-rendered value %q:\n%s", unexpected, rendered)
		}
	}
}

func TestRenderMermaidQuadrantRejectsReferenceParseErrors(t *testing.T) {
	for _, source := range []string{
		"quadrantChart",
		"quadrantChart\nx-axis left-right\nA: [0, 0]",
		"quadrantChart\nquadrant-x Bad\nA: [0, 0]",
		"quadrantChart\nquadrant-1\nA: [0, 0]",
		"quadrantChart\nA 0, 0",
		"quadrantChart\nA: 0, 0",
		"quadrantChart\nA: [0]",
		"quadrantChart\nA: [0, nope]",
	} {
		if _, ok := renderMermaid(source, 40, paletteFor("groknight")); ok {
			t.Fatalf("invalid quadrantChart was accepted: %q", source)
		}
	}
}

func TestRenderMarkdownRendersClosedMermaidQuadrant(t *testing.T) {
	source := "Before\n```mermaid\nquadrantChart\nquadrant-1 Ship\nIdea: [0.7, 0.8]\n```\nAfter"
	rendered := stripUIANSI(strings.Join(renderMarkdown(source, 36), "\n"))
	for _, expected := range []string{"Before", "◇ mermaid quadrant", "Q1: Ship", "Q1 • Idea [0.7, 0.8]", "After"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered Mermaid quadrantChart missing %q:\n%s", expected, rendered)
		}
	}
	if strings.Contains(rendered, "Idea: [0.7, 0.8]") {
		t.Fatalf("closed Mermaid quadrantChart source was not replaced:\n%s", rendered)
	}
}

func TestRenderMermaidSankeyMatchesReferenceParsing(t *testing.T) {
	source := "%% comment\nsankey-beta extra\n,B,-1\nB,A,1e3\nA,B,2.5"
	lines, ok := renderMermaid(source, 40, paletteFor("groknight"))
	if !ok {
		t.Fatal("reference-compatible Mermaid sankey was rejected")
	}
	rendered := stripUIANSI(strings.Join(lines, "\n"))
	for _, expected := range []string{
		"nodes:  · B · A", " ─-1▶ B", "B ─1000▶ A", "A ─2.5▶ B",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("sankey parsing differs from reference, missing %q:\n%s", expected, rendered)
		}
	}
	if strings.Count(rendered, " · A") != 1 {
		t.Fatalf("sankey node first-seen order was not deduplicated:\n%s", rendered)
	}
}

func TestRenderMermaidSankeyRejectsReferenceParseErrors(t *testing.T) {
	for _, source := range []string{
		"sankey-beta",
		"sankey-beta\nA,B",
		"sankey-beta\nA,B,1,extra",
		"sankey-beta\nA,B,nope",
	} {
		if _, ok := renderMermaid(source, 40, paletteFor("groknight")); ok {
			t.Fatalf("invalid sankey was accepted: %q", source)
		}
	}
}

func TestRenderMarkdownRendersClosedMermaidSankey(t *testing.T) {
	source := "Before\n```mermaid\nsankey-beta\nSource,Target,3\n```\nAfter"
	rendered := stripUIANSI(strings.Join(renderMarkdown(source, 36), "\n"))
	for _, expected := range []string{"Before", "◇ mermaid sankey", "nodes: Source · Target", "Source ─3▶ Target", "After"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered Mermaid sankey missing %q:\n%s", expected, rendered)
		}
	}
	if strings.Contains(rendered, "Source,Target,3") {
		t.Fatalf("closed Mermaid sankey source was not replaced:\n%s", rendered)
	}
}

func TestRenderMermaidGitGraphMatchesReferenceBranchState(t *testing.T) {
	source := "gitGraph\ncommit\nbranch topic\ncommit\ncheckout missing\ncommit\nbranch topic\ncheckout main\nmerge unknown"
	lines, ok := renderMermaid(source, 48, paletteFor("groknight"))
	if !ok {
		t.Fatal("reference branch-state gitGraph was rejected")
	}
	rendered := stripUIANSI(strings.Join(lines, "\n"))
	for _, expected := range []string{
		"branches: main · topic · missing",
		"● 0 main", "● 1 main ← 0", "● 2 missing", "◎ 3 main ← 1",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("gitGraph state differs from reference, missing %q:\n%s", expected, rendered)
		}
	}
	if strings.Count(rendered, "topic") != 1 {
		t.Fatalf("branch declaration unexpectedly checked out or duplicated topic:\n%s", rendered)
	}
}

func TestRenderMermaidGitGraphAcceptsEmptyGraph(t *testing.T) {
	lines, ok := renderMermaid("%% comment\ngitGraph", 24, paletteFor("groknight"))
	if !ok {
		t.Fatal("empty reference gitGraph was rejected")
	}
	rendered := stripUIANSI(strings.Join(lines, "\n"))
	if !strings.Contains(rendered, "branches: main") {
		t.Fatalf("empty gitGraph lost main branch:\n%s", rendered)
	}
}

func TestRenderMermaidGitGraphRejectsReferenceParseErrors(t *testing.T) {
	for _, source := range []string{
		"gitGraph\nbranch",
		"gitGraph\ncheckout",
		"gitGraph\nmerge",
		"gitGraph\nreset main",
	} {
		if _, ok := renderMermaid(source, 40, paletteFor("groknight")); ok {
			t.Fatalf("invalid gitGraph was accepted: %q", source)
		}
	}
}

func TestRenderMarkdownRendersClosedMermaidGitGraph(t *testing.T) {
	source := "Before\n```mermaid\ngitGraph\ncommit\nbranch dev\ncheckout dev\ncommit\n```\nAfter"
	rendered := stripUIANSI(strings.Join(renderMarkdown(source, 40), "\n"))
	for _, expected := range []string{"Before", "◇ mermaid gitGraph", "branches: main · dev", "● 0 main", "● 1 dev ← 0", "After"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered Mermaid gitGraph missing %q:\n%s", expected, rendered)
		}
	}
	if strings.Contains(rendered, "checkout dev") {
		t.Fatalf("closed Mermaid gitGraph source was not replaced:\n%s", rendered)
	}
}

func TestRenderMermaidPacketSplitsBlocksAtReferenceRowBoundary(t *testing.T) {
	lines, ok := renderMermaid("packet-beta\n16-47: Wide field", 32, paletteFor("groknight"))
	if !ok {
		t.Fatal("cross-row Mermaid packet was rejected")
	}
	rendered := stripUIANSI(strings.Join(lines, "\n"))
	for _, expected := range []string{"16-31 │ Wide field", "32-47 │ Wide field"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("packet boundary split missing %q:\n%s", expected, rendered)
		}
	}
}

func TestRenderMermaidPacketRejectsReferenceParseErrors(t *testing.T) {
	for _, source := range []string{
		"packet-beta",
		"packet-beta\n0-3 Header",
		"packet-beta\n3-1: Backwards",
		"packet-beta\n0-3: First\n5-7: Gap",
		"packet-beta\n0-3:",
		"packet-beta\n-1: Negative",
	} {
		if _, ok := renderMermaid(source, 40, paletteFor("groknight")); ok {
			t.Fatalf("invalid packet was accepted: %q", source)
		}
	}
}

func TestRenderMermaidPacketAcceptsFullReferenceBitRange(t *testing.T) {
	lines, ok := renderMermaid("packet-beta\n4294967295: Max bit", 32, paletteFor("groknight"))
	if !ok {
		t.Fatal("full u32 packet bit range was rejected")
	}
	rendered := stripUIANSI(strings.Join(lines, "\n"))
	if !strings.Contains(rendered, "4294967295 │ Max bit") {
		t.Fatalf("full u32 packet bit was not preserved:\n%s", rendered)
	}
}

func TestRenderMermaidPacketKeepsBoundedOutput(t *testing.T) {
	var statements strings.Builder
	statements.WriteString("packet-beta\n")
	for bit := 0; bit < maxMermaidStatements; bit++ {
		statements.WriteString(strconv.Itoa(bit))
		statements.WriteString(": Bit\n")
	}
	if _, ok := renderMermaid(statements.String(), 40, paletteFor("groknight")); ok {
		t.Fatal("packet over the statement limit was accepted")
	}
	if _, ok := renderMermaid("packet-beta\n0-20000: Huge", 40, paletteFor("groknight")); ok {
		t.Fatal("packet over the rendered block limit was accepted")
	}
}

func TestRenderMarkdownRendersClosedMermaidPacket(t *testing.T) {
	source := "Before\n```mermaid\npacket-beta\n0-3: Header\n4-7: Data\n```\nAfter"
	rendered := stripUIANSI(strings.Join(renderMarkdown(source, 32), "\n"))
	for _, expected := range []string{"Before", "◇ mermaid packet", "0-3 │ Header", "4-7 │ Data", "After"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered Mermaid packet missing %q:\n%s", expected, rendered)
		}
	}
	if strings.Contains(rendered, "0-3: Header") || strings.Contains(rendered, "4-7: Data") {
		t.Fatalf("closed Mermaid packet source was not replaced:\n%s", rendered)
	}
}

func TestRenderMarkdownRendersClosedMermaidPie(t *testing.T) {
	source := "Before\n```mermaid\npie\nA : 2\nB : 1\n```\nAfter"
	rendered := stripUIANSI(strings.Join(renderMarkdown(source, 32), "\n"))
	for _, expected := range []string{"Before", "◇ mermaid pie", "A", "67%", "B", "33%", "After"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered Mermaid pie missing %q:\n%s", expected, rendered)
		}
	}
	if strings.Contains(rendered, "A : 2") || strings.Contains(rendered, "B : 1") {
		t.Fatalf("closed Mermaid pie source was not replaced:\n%s", rendered)
	}
}

func TestRenderMermaidPieFitsNarrowWidthsAndZeroSlices(t *testing.T) {
	lines, ok := renderMermaid("pie\nLong label : 1\nZero : 0", 5, paletteFor("groknight"))
	if !ok {
		t.Fatal("valid narrow Mermaid pie was rejected")
	}
	rendered := stripUIANSI(strings.Join(lines, "\n"))
	if !strings.Contains(rendered, "100%") || !strings.Contains(rendered, "0%") {
		t.Fatalf("narrow pie lost percentages:\n%s", rendered)
	}
	for _, line := range lines {
		if displayWidth(stripUIANSI(line)) > 5 {
			t.Fatalf("narrow pie line exceeds width: %q", stripUIANSI(line))
		}
	}
}

func TestRenderMermaidPieRejectsInvalidCharts(t *testing.T) {
	for _, source := range []string{
		"pie",
		"pie\nA : 0",
		"pie\nA : -1\nB : 2",
		"pie\nA : nope",
	} {
		if _, ok := renderMermaid(source, 40, paletteFor("groknight")); ok {
			t.Fatalf("invalid pie was accepted: %q", source)
		}
	}
}

func TestRenderMermaidTimeline(t *testing.T) {
	source := "timeline\ntitle Release history\nsection Build\n2025 : Prototype\n: Tests\nsection Operate\nNow\nsection Build\n2026 : Launch"
	lines, ok := renderMermaid(source, 28, paletteFor("groknight"))
	if !ok {
		t.Fatal("supported Mermaid timeline was rejected")
	}
	rendered := stripUIANSI(strings.Join(lines, "\n"))
	for _, expected := range []string{"◇ mermaid timeline", "Release history", "Build", "2025", "Prototype", "Tests", "2026", "Launch", "Operate", "Now"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered timeline missing %q:\n%s", expected, rendered)
		}
	}
	if strings.Index(rendered, "2025") > strings.Index(rendered, "2026") || strings.Index(rendered, "2026") > strings.Index(rendered, "Now") {
		t.Fatalf("timeline order changed:\n%s", rendered)
	}
	for _, line := range lines {
		if displayWidth(stripUIANSI(line)) > 28 {
			t.Fatalf("timeline line exceeds width: %q", stripUIANSI(line))
		}
	}
}

func TestRenderMarkdownRendersClosedMermaidTimeline(t *testing.T) {
	source := "Before\n```mermaid\ntimeline\n2025 : Build\n2026 : Ship\n```\nAfter"
	rendered := stripUIANSI(strings.Join(renderMarkdown(source, 32), "\n"))
	for _, expected := range []string{"Before", "◇ mermaid timeline", "2025", "Build", "2026", "Ship", "After"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered Mermaid timeline missing %q:\n%s", expected, rendered)
		}
	}
	if strings.Contains(rendered, "2025 : Build") || strings.Contains(rendered, "2026 : Ship") {
		t.Fatalf("closed Mermaid timeline source was not replaced:\n%s", rendered)
	}
}

func TestRenderMermaidTimelineRejectsEmptyInput(t *testing.T) {
	for _, source := range []string{
		"timeline",
		"timeline\ntitle Empty",
		"timeline\n%% comment\n# comment",
	} {
		if _, ok := renderMermaid(source, 40, paletteFor("groknight")); ok {
			t.Fatalf("empty timeline was accepted: %q", source)
		}
	}
}

func TestRenderMermaidTimelineIgnoresEntriesOutsideSections(t *testing.T) {
	lines, ok := renderMermaid("timeline\nBefore\nsection Later\nAfter", 40, paletteFor("groknight"))
	if !ok {
		t.Fatal("sectioned timeline was rejected")
	}
	rendered := stripUIANSI(strings.Join(lines, "\n"))
	if strings.Contains(rendered, "Before") || !strings.Contains(rendered, "Later") || !strings.Contains(rendered, "After") {
		t.Fatalf("section filtering differs from the reference:\n%s", rendered)
	}
}

func TestRenderMermaidJourney(t *testing.T) {
	source := "journey\ntitle My working day\nsection Go to work\nMake tea: 5: Me\nGo upstairs: 3: Me, Team\nsection Go home\nGo downstairs: 4\nsection Go to work\nReturn: 2"
	lines, ok := renderMermaid(source, 32, paletteFor("groknight"))
	if !ok {
		t.Fatal("supported Mermaid journey was rejected")
	}
	rendered := stripUIANSI(strings.Join(lines, "\n"))
	for _, expected := range []string{"◇ mermaid journey", "My working day", "Go to work", "Make tea [5]", "Me", "Go upstairs [3]", "Team", "Go home", "Go downstairs [4]", "Return [2]"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered journey missing %q:\n%s", expected, rendered)
		}
	}
	if strings.Count(rendered, "Go to work") != 2 || strings.Index(rendered, "Go downstairs") > strings.Index(rendered, "Return") {
		t.Fatalf("journey section order changed:\n%s", rendered)
	}
	for _, line := range lines {
		if displayWidth(stripUIANSI(line)) > 32 {
			t.Fatalf("journey line exceeds width: %q", stripUIANSI(line))
		}
	}
}

func TestRenderMermaidGantt(t *testing.T) {
	source := "gantt\ntitle Release\ndateFormat YYYY-MM-DD\nsection Build\nSetup :a1, 2026-01-01, 2d\nImplement :a2, after a1, 1w\nsection Ship\nRelease :a3, after a2, 1d"
	lines, ok := renderMermaid(source, 48, paletteFor("groknight"))
	if !ok {
		t.Fatal("supported Mermaid gantt was rejected")
	}
	rendered := stripUIANSI(strings.Join(lines, "\n"))
	for _, expected := range []string{"◇ mermaid gantt", "Release", "Build", "Setup", "2026-01-01", "2026-01-03", "Implement", "2026-01-10", "Ship", "2026-01-11"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered gantt missing %q:\n%s", expected, rendered)
		}
	}
	for _, line := range lines {
		if displayWidth(stripUIANSI(line)) > 48 {
			t.Fatalf("gantt line exceeds width: %q", stripUIANSI(line))
		}
	}
}

func TestRenderMermaidGanttNormalizesReferenceDates(t *testing.T) {
	lines, ok := renderMermaid("gantt\nTask :a1, 2026-13-01, 1d", 60, paletteFor("groknight"))
	if !ok {
		t.Fatal("reference-compatible normalized date was rejected")
	}
	rendered := stripUIANSI(strings.Join(lines, "\n"))
	if !strings.Contains(rendered, "2027-01-01") || !strings.Contains(rendered, "2027-01-02") {
		t.Fatalf("date was not normalized:\n%s", rendered)
	}
}

func TestRenderMermaidJourneyAndGanttRejectInvalidInput(t *testing.T) {
	for _, source := range []string{
		"journey\ntitle Empty",
		"journey\nTask only",
		"journey\nTask: nope: Me",
		"gantt\ntitle Empty",
		"gantt\nTask :a1, 2026-01-01",
		"gantt\nTask :a1, invalid, 2d",
		"gantt\nTask :a1, after missing, 2d",
		"gantt\nTask :a1, 2026-01-01, 2h",
	} {
		if _, ok := renderMermaid(source, 60, paletteFor("groknight")); ok {
			t.Fatalf("invalid diagram was accepted: %q", source)
		}
	}
}

func TestRenderMarkdownRendersClosedMermaidJourneyAndGantt(t *testing.T) {
	for _, test := range []struct {
		source string
		want   string
	}{
		{source: "```mermaid\njourney\nTask: 5: Me\n```", want: "Task [5]"},
		{source: "```mermaid\ngantt\nTask :a1, 2026-01-01, 2d\n```", want: "2026-01-03"},
	} {
		rendered := stripUIANSI(strings.Join(renderMarkdown(test.source, 60), "\n"))
		if !strings.Contains(rendered, test.want) || strings.Contains(rendered, "Task :") {
			t.Fatalf("closed Mermaid source was not replaced:\n%s", rendered)
		}
	}
}

func TestRenderMarkdownKeepsIncompleteAndUnsupportedMermaidSource(t *testing.T) {
	for _, test := range []struct {
		source string
		want   string
	}{
		{source: "```mermaid\nflowchart TD\nA --> B", want: "A"},
		{source: "```mermaid\npie\n  \"A\" : nope\n```", want: "A"},
		{source: "```mermaid\ntimeline\ntitle Empty\n```", want: "Empty"},
		{source: "```mermaid\njourney\nTask only\n```", want: "Task only"},
		{source: "```mermaid\ngantt\nTask :a1, bad, 2d\n```", want: "bad"},
	} {
		rendered := stripUIANSI(strings.Join(renderMarkdown(test.source, 80), "\n"))
		if strings.Contains(rendered, "◇ mermaid") || !strings.Contains(rendered, "mermaid") || !strings.Contains(rendered, test.want) {
			t.Fatalf("source fallback failed:\n%s", rendered)
		}
	}
}

func TestRenderMarkdownHonorsDisabledMermaidRendering(t *testing.T) {
	theme := paletteFor("groknight")
	theme.mermaid = false
	source := "```mermaid\nflowchart TD\nA --> B\n```"
	rendered := stripUIANSI(strings.Join(renderMarkdownTheme(source, 80, false, theme), "\n"))
	if strings.Contains(rendered, "◇ mermaid") || !strings.Contains(rendered, "flowchart TD") || !strings.Contains(rendered, "A --> B") {
		t.Fatalf("disabled Mermaid rendering did not keep source:\n%s", rendered)
	}
}

func TestRenderMarkdownRendersQuotedAndLongMermaidFences(t *testing.T) {
	source := "> ````Mermaid theme=base\n> flowchart LR\n> A[Quoted] --> B[Diagram]\n> ````"
	rendered := stripUIANSI(strings.Join(renderMarkdown(source, 50), "\n"))
	for _, expected := range []string{"◇ mermaid", "Quoted", "Diagram"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("quoted Mermaid missing %q:\n%s", expected, rendered)
		}
	}
	if strings.Contains(rendered, "flowchart LR") {
		t.Fatalf("quoted Mermaid source was not replaced:\n%s", rendered)
	}
}

func TestRenderMermaidFallsBackBelowMinimumWidth(t *testing.T) {
	if _, ok := renderMermaid("flowchart TD\nA --> B", 4, paletteFor("groknight")); ok {
		t.Fatal("diagram renderer accepted a width too small for a box")
	}
}

func TestRenderMermaidSanitizesTerminalControlCharacters(t *testing.T) {
	lines, ok := renderMermaid("flowchart TD\nA[unsafe\x1b[31m] --> B", 40, paletteFor("groknight"))
	if !ok {
		t.Fatal("diagram was rejected")
	}
	rendered := strings.Join(lines, "\n")
	plain := stripUIANSI(rendered)
	if strings.ContainsRune(plain, '\x1b') || !strings.Contains(plain, "unsafe [31m") {
		t.Fatalf("untrusted terminal control survived: %q", rendered)
	}
}

func TestRenderMermaidRejectsOversizedInputWithoutPartialOutput(t *testing.T) {
	var source strings.Builder
	source.WriteString("flowchart TD\n")
	for index := 0; index < maxMermaidStatements; index++ {
		source.WriteString("A --> B\n")
	}
	if _, ok := renderMermaid(source.String(), 40, paletteFor("groknight")); ok {
		t.Fatal("statement limit produced a partial diagram")
	}
	if _, ok := renderMermaid("flowchart TD\n"+strings.Repeat("A", maxMermaidSource), 40, paletteFor("groknight")); ok {
		t.Fatal("source size limit produced a partial diagram")
	}
}
