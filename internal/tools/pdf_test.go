package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/workspace"
)

func makePDF(pageTexts ...string) []byte {
	var document bytes.Buffer
	document.WriteString("%PDF-1.4\n")
	var offsets []int
	writeObject := func(value string) {
		offsets = append(offsets, document.Len())
		document.WriteString(value)
	}
	writeObject("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	var kids []string
	for index := range pageTexts {
		kids = append(kids, fmt.Sprintf("%d 0 R", 3+index*3))
	}
	writeObject(fmt.Sprintf("2 0 obj\n<< /Type /Pages /Kids [%s] /Count %d >>\nendobj\n", strings.Join(kids, " "), len(pageTexts)))
	for index, text := range pageTexts {
		pageObject, contentObject, fontObject := 3+index*3, 4+index*3, 5+index*3
		stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
		writeObject(fmt.Sprintf("%d 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 %d 0 R >> >> >>\nendobj\n", pageObject, contentObject, fontObject))
		writeObject(fmt.Sprintf("%d 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", contentObject, len(stream), stream))
		writeObject(fmt.Sprintf("%d 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n", fontObject))
	}
	xref := document.Len()
	totalObjects := len(offsets) + 1
	fmt.Fprintf(&document, "xref\n0 %d\n0000000000 65535 f \n", totalObjects)
	for _, offset := range offsets {
		fmt.Fprintf(&document, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&document, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", totalObjects, xref)
	return document.Bytes()
}

func TestExtractPDFTextSelectsPages(t *testing.T) {
	text, err := extractPDFText(makePDF("First", "Second", "Third"), "3, 2")
	if err != nil {
		t.Fatal(err)
	}
	if text != "--- Page 2 ---\nSecond\n--- Page 3 ---\nThird" {
		t.Fatalf("unexpected PDF text: %q", text)
	}
}

func TestParsePDFPagesLimitsRanges(t *testing.T) {
	pages, err := parsePDFPages("2-4,3,6-", 7)
	if err != nil || fmt.Sprint(pages) != "[2 3 4 6 7]" {
		t.Fatalf("unexpected pages=%v err=%v", pages, err)
	}
	if _, err := parsePDFPages("", 11); err == nil {
		t.Fatal("auto-read accepted more than ten pages")
	}
	if _, err := parsePDFPages("1-21", 21); err == nil {
		t.Fatal("page selection accepted more than twenty pages")
	}
	for _, spec := range []string{"0", "8", "4-2", "x"} {
		if _, err := parsePDFPages(spec, 7); err == nil {
			t.Errorf("invalid page spec %q was accepted", spec)
		}
	}
}

func TestReadFileExtractsPDFText(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"doc.pdf", "document-without-extension"} {
		if err := os.WriteFile(filepath.Join(root, name), makePDF("Hello PDF"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	tool := &readFileTool{ws: ws}
	output, err := tool.Execute(context.Background(), json.RawMessage(`{"target_file":"doc.pdf","format":"text"}`))
	if err != nil {
		t.Fatal(err)
	}
	if output != "1→--- Page 1 ---\n2→Hello PDF\n" {
		t.Fatalf("unexpected read_file PDF output: %q", output)
	}
	output, err = tool.Execute(context.Background(), json.RawMessage(`{"target_file":"document-without-extension","format":"text"}`))
	if err != nil || !strings.Contains(output, "Hello PDF") {
		t.Fatalf("PDF magic-byte detection failed: output=%q err=%v", output, err)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"target_file":"doc.pdf"}`)); err == nil || !strings.Contains(err.Error(), "format \"text\"") {
		t.Fatalf("default image mode should remain explicit, got %v", err)
	}
}

func TestReadFileRendersPDFPages(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm is not installed")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "doc.pdf"), makePDF("First", "Second"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	result, err := registry.ExecuteResult(context.Background(), "read_file", json.RawMessage(`{"target_file":"doc.pdf","pages":"2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "[PDF: doc.pdf (1 pages rendered, 2 total)]" || len(result.Images) != 1 || result.Images[0].MediaType != "image/jpeg" || result.Images[0].Width < 1 || result.Images[0].Height < 1 {
		t.Fatalf("unexpected PDF render result: %#v", result)
	}
}
