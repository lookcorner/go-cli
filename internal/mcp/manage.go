package mcp

import (
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"unicode"
)

func ParseServerInput(source, name string) (ServerConfig, error) {
	source, name = strings.TrimSpace(source), strings.TrimSpace(name)
	if source == "" {
		return ServerConfig{}, errors.New("MCP server URL or command is required")
	}
	parsed, parseErr := url.Parse(source)
	if parseErr == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		if parsed.Host == "" {
			return ServerConfig{}, errors.New("MCP server URL requires a host")
		}
		if name == "" {
			name = parsed.Hostname()
		}
		transport := "http"
		if strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/sse") {
			transport = "sse"
		}
		return ServerConfig{Type: transport, Name: name, URL: source}, nil
	}
	if strings.Contains(source, "://") {
		if parseErr != nil {
			return ServerConfig{}, errors.New("MCP server URL is invalid")
		}
		return ServerConfig{}, errors.New("MCP server URL must use HTTP or HTTPS")
	}
	words, err := splitCommand(source)
	if err != nil {
		return ServerConfig{}, err
	}
	if len(words) == 0 {
		return ServerConfig{}, errors.New("MCP server command is required")
	}
	if name == "" {
		commandName := filepath.Base(strings.ReplaceAll(words[0], "\\", "/"))
		name = strings.TrimSuffix(commandName, filepath.Ext(commandName))
	}
	if name == "" || name == "." {
		return ServerConfig{}, errors.New("MCP server name is required")
	}
	return ServerConfig{Type: "stdio", Name: name, Command: words[0], Args: words[1:]}, nil
}

func splitCommand(value string) ([]string, error) {
	var words []string
	var current strings.Builder
	var quote rune
	escaped, started := false, false
	flush := func() {
		if started {
			words = append(words, current.String())
			current.Reset()
			started = false
		}
	}
	for _, char := range value {
		if escaped {
			current.WriteRune(char)
			escaped, started = false, true
			continue
		}
		if char == '\\' && quote != '\'' {
			escaped, started = true, true
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			} else {
				current.WriteRune(char)
			}
			started = true
			continue
		}
		switch {
		case char == '\'' || char == '"':
			quote, started = char, true
		case unicode.IsSpace(char):
			flush()
		default:
			current.WriteRune(char)
			started = true
		}
	}
	if escaped {
		return nil, errors.New("MCP server command ends with an escape")
	}
	if quote != 0 {
		return nil, errors.New("MCP server command has an unterminated quote")
	}
	flush()
	return words, nil
}
