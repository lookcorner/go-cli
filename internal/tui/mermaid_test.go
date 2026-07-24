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

func TestRenderMarkdownKeepsIncompleteAndUnsupportedMermaidSource(t *testing.T) {
	for _, source := range []string{
		"```mermaid\nflowchart TD\nA --> B",
		"```mermaid\npie\n  \"A\" : 1\n```",
	} {
		rendered := stripUIANSI(strings.Join(renderMarkdown(source, 80), "\n"))
		if strings.Contains(rendered, "◇ mermaid") || !strings.Contains(rendered, "mermaid") || !strings.Contains(rendered, "A") {
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
