package mcp

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseServerInput(t *testing.T) {
	tests := []struct {
		name, source, override string
		want                   ServerConfig
	}{
		{"http", "https://mcp.example/v1", "", ServerConfig{Type: "http", Name: "mcp.example", URL: "https://mcp.example/v1"}},
		{"sse", "http://localhost:3000/events/sse/", "local", ServerConfig{Type: "sse", Name: "local", URL: "http://localhost:3000/events/sse/"}},
		{"command", `npx -y "@scope/server" --root '/tmp/my files'`, "", ServerConfig{Type: "stdio", Name: "npx", Command: "npx", Args: []string{"-y", "@scope/server", "--root", "/tmp/my files"}}},
		{"escaped", `C:\\tools\\mcp.exe --label one\ two ""`, "windows", ServerConfig{Type: "stdio", Name: "windows", Command: `C:\tools\mcp.exe`, Args: []string{"--label", "one two", ""}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseServerInput(test.source, test.override)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got=%#v want=%#v", got, test.want)
			}
		})
	}
}

func TestParseServerInputRejectsInvalidInput(t *testing.T) {
	for _, source := range []string{"", "https:///missing", "ftp://mcp.example", "https://bad%zz", `command "unfinished`, `command trailing\`} {
		if _, err := ParseServerInput(source, ""); err == nil || !strings.Contains(err.Error(), "MCP server") {
			t.Errorf("source=%q err=%v", source, err)
		}
	}
}
