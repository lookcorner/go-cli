package tui

import (
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

func TestRenderMarkdownKeepsIncompleteAndUnsupportedMermaidSource(t *testing.T) {
	for _, test := range []struct {
		source string
		want   string
	}{
		{source: "```mermaid\nflowchart TD\nA --> B", want: "A"},
		{source: "```mermaid\npie\n  \"A\" : nope\n```", want: "A"},
		{source: "```mermaid\ntimeline\ntitle Empty\n```", want: "Empty"},
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
