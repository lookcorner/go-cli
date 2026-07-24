package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/mcpadmin"
)

func TestMCPCLIAddListRemove(t *testing.T) {
	root := t.TempDir()
	userPath := filepath.Join(t.TempDir(), "config.toml")
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	var stdout, stderr bytes.Buffer
	if err := runMCP([]string{"add", "--config", userPath, "-e", "TOKEN=secret", "local", "--", "npx", "-y", "server"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "secret") || !strings.Contains(stdout.String(), "Added stdio MCP server") {
		t.Fatalf("add output=%s", stdout.String())
	}
	stdout.Reset()
	if err := runMCP([]string{"list", "--config", userPath, "--json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var flat []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &flat); err != nil || len(flat) != 1 || flat[0]["name"] != "local" || flat[0]["command"] != "npx" || flat[0]["config"] != nil {
		t.Fatalf("entries=%#v err=%v output=%s", flat, err, stdout.String())
	}
	stdout.Reset()
	if err := runMCP([]string{"remove", "local", "--config", userPath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Removed MCP server") {
		t.Fatalf("remove output=%s", stdout.String())
	}
}

func TestMCPCLIAddsRemoteServerAndValidatesArguments(t *testing.T) {
	userPath := filepath.Join(t.TempDir(), "config.toml")
	var stdout bytes.Buffer
	if err := runMCP([]string{"add", "--config", userPath, "--transport", "http", "-H", "Authorization: secret", "remote", "https://mcp.example/api"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(userPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPServers["remote"].URL != "https://mcp.example/api" || cfg.MCPServers["remote"].Headers["Authorization"] != "secret" {
		t.Fatalf("config=%#v", cfg.MCPServers["remote"])
	}
	for _, args := range [][]string{
		{"add", "bad name", "--", "server"},
		{"add", "--transport", "http", "remote", "bad-url"},
		{"remove"},
		{"doctor", "one", "two"},
	} {
		if err := runMCP(args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted args=%v", args)
		}
	}
}

func TestMCPCLIDoctorJSONAndFailureExit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	userPath := filepath.Join(t.TempDir(), "config.toml")
	if err := config.UpsertMCPServer(userPath, "good", config.MCPServerConfig{Command: "good"}); err != nil {
		t.Fatal(err)
	}
	if err := config.UpsertMCPServer(userPath, "bad", config.MCPServerConfig{Command: "bad"}); err != nil {
		t.Fatal(err)
	}
	previousProbe := probeMCPServer
	probeMCPServer = func(_ context.Context, name string, _ config.MCPServerConfig, _ string) (mcpadmin.ProbeResult, error) {
		if name == "bad" {
			return mcpadmin.ProbeResult{}, errors.New("unavailable")
		}
		return mcpadmin.ProbeResult{ProtocolVersion: "2025-11-25", ServerName: "test", ToolCount: 3}, nil
	}
	t.Cleanup(func() { probeMCPServer = previousProbe })

	var stdout bytes.Buffer
	err := runMCP([]string{"doctor", "--config", userPath, "--json"}, &stdout, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "1 MCP server") {
		t.Fatalf("doctor error=%v output=%s", err, stdout.String())
	}
	var report mcpadmin.DoctorReport
	if json.Unmarshal(stdout.Bytes(), &report) != nil || report.FailingCount != 1 || report.HealthyCount != 1 || len(report.Servers) != 2 {
		t.Fatalf("report=%#v output=%s", report, stdout.String())
	}
}

func TestParseMCPAddSupportsFlagsAfterName(t *testing.T) {
	parsed, err := parseMCPAdd([]string{"remote", "https://mcp.example", "--transport", "sse", "--scope", "project", "--header", "X-Test: yes"})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.request.Name != "remote" || parsed.request.Source != "https://mcp.example" ||
		parsed.request.Transport != "sse" || parsed.request.Scope != mcpadmin.ProjectScope ||
		parsed.request.Headers["X-Test"] != "yes" {
		t.Fatalf("parsed=%#v", parsed)
	}
}

func TestMCPRemoveAndDoctorSupportFlagsAfterName(t *testing.T) {
	name, scope, path, err := parseMCPRemove([]string{"server", "--scope", "project", "--config", "custom.toml"})
	if err != nil || name != "server" || scope != "project" || path != "custom.toml" {
		t.Fatalf("remove name=%q scope=%q path=%q err=%v", name, scope, path, err)
	}
	name, asJSON, path, err := parseMCPDoctor([]string{"server", "--json", "--config", "custom.toml"})
	if err != nil || name != "server" || !asJSON || path != "custom.toml" {
		t.Fatalf("doctor name=%q json=%v path=%q err=%v", name, asJSON, path, err)
	}
}

func TestMCPSubcommandHelp(t *testing.T) {
	for _, command := range []string{"list", "add", "remove", "doctor"} {
		var output bytes.Buffer
		if err := runMCP([]string{command, "--help"}, &output, &bytes.Buffer{}); err != nil {
			t.Fatalf("%s help: %v", command, err)
		}
		if !strings.Contains(output.String(), "Usage: gork mcp "+command) {
			t.Fatalf("%s help=%q", command, output.String())
		}
	}
}
