package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/workspace"
)

func makePPTX(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for name, content := range entries {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func slideXML(paragraphs ...string) string {
	var body strings.Builder
	for _, paragraph := range paragraphs {
		body.WriteString("<a:p><a:r><a:t>")
		body.WriteString(paragraph)
		body.WriteString("</a:t></a:r></a:p>")
	}
	return `<p:sld xmlns:a="a" xmlns:p="p">` + body.String() + `</p:sld>`
}

func TestExtractPPTXTextOrdersSlidesAndIncludesNotes(t *testing.T) {
	data := makePPTX(t, map[string]string{
		"ppt/slides/slide10.xml":          slideXML("Tenth"),
		"ppt/slides/slide2.xml":           slideXML("Second"),
		"ppt/slides/slide1.xml":           slideXML("Title", "Body &amp; details"),
		"ppt/notesSlides/notesSlide2.xml": slideXML("A note"),
	})
	text, err := extractPPTXText(data)
	if err != nil {
		t.Fatal(err)
	}
	want := "--- Slide 1 ---\nTitle\nBody & details\n\n--- Slide 2 ---\nSecond\n\nSpeaker Notes:\nA note\n\n--- Slide 10 ---\nTenth"
	if text != want {
		t.Fatalf("unexpected PPTX text:\n%s\nwant:\n%s", text, want)
	}
}

func TestReadFileExtractsPPTXAsNumberedText(t *testing.T) {
	root := t.TempDir()
	data := makePPTX(t, map[string]string{"ppt/slides/slide1.xml": slideXML("Title", "Body")})
	if err := os.WriteFile(filepath.Join(root, "deck.pptx"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	output, err := (&readFileTool{ws: ws}).Execute(context.Background(), json.RawMessage(`{"target_file":"deck.pptx"}`))
	if err != nil {
		t.Fatal(err)
	}
	if output != "1→--- Slide 1 ---\n2→Title\n3→Body\n" {
		t.Fatalf("unexpected read_file PPTX output: %q", output)
	}
}

func TestExtractPPTXTextRejectsInvalidArchives(t *testing.T) {
	if _, err := extractPPTXText([]byte("not a zip")); err == nil {
		t.Fatal("invalid PPTX archive was accepted")
	}
	if _, err := extractPPTXText(makePPTX(t, map[string]string{"doc.xml": "<doc/>"})); err == nil {
		t.Fatal("PPTX without slides was accepted")
	}
}
