package mcp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
)

var (
	activeMCPAuthMu sync.Mutex
	activeMCPAuth   = map[string]chan<- oauthCallback{}
)

// SubmitMCPAuthCallback delivers a pasted callback URL or code+state to an
// in-flight enrollment for serverName. Used when the loopback redirect is
// unreachable (remote/headless clients).
func SubmitMCPAuthCallback(serverName, value string) error {
	serverName = strings.TrimSpace(serverName)
	if serverName == "" {
		return errors.New("MCP server name is required")
	}
	activeMCPAuthMu.Lock()
	callback := activeMCPAuth[serverName]
	activeMCPAuthMu.Unlock()
	if callback == nil {
		return errors.New("MCP OAuth enrollment is not active for " + serverName)
	}
	result := parseMCPCallback(value)
	select {
	case callback <- result:
		return nil
	default:
		return errors.New("MCP OAuth callback was already submitted")
	}
}

func registerMCPAuthSubmit(serverName string, callback chan<- oauthCallback) func() {
	activeMCPAuthMu.Lock()
	activeMCPAuth[serverName] = callback
	activeMCPAuthMu.Unlock()
	return func() {
		activeMCPAuthMu.Lock()
		if activeMCPAuth[serverName] == callback {
			delete(activeMCPAuth, serverName)
		}
		activeMCPAuthMu.Unlock()
	}
}

func readPastedMCPCallback(input io.Reader, callback chan<- oauthCallback) {
	if input == nil {
		return
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if value := strings.TrimSpace(scanner.Text()); value != "" {
			select {
			case callback <- parseMCPCallback(value):
			default:
			}
			return
		}
	}
}

func parseMCPCallback(value string) oauthCallback {
	value = strings.TrimSpace(value)
	if value == "" {
		return oauthCallback{err: errors.New("MCP OAuth callback is empty")}
	}
	if parsed, err := url.Parse(value); err == nil && (parsed.IsAbs() || parsed.RawQuery != "" || strings.Contains(value, "code=")) {
		query := parsed.Query()
		if query.Get("code") == "" && parsed.Fragment != "" {
			if frag, err := url.ParseQuery(strings.TrimPrefix(parsed.Fragment, "?")); err == nil {
				query = frag
			}
		}
		if errMsg := query.Get("error"); errMsg != "" {
			desc := query.Get("error_description")
			if desc == "" {
				desc = errMsg
			}
			return oauthCallback{err: fmt.Errorf("MCP OAuth denied: %s", desc)}
		}
		code := query.Get("code")
		state := query.Get("state")
		if code == "" {
			return oauthCallback{err: errors.New("MCP OAuth callback URL has no code")}
		}
		if state == "" {
			return oauthCallback{err: errors.New("MCP OAuth callback URL has no state")}
		}
		return oauthCallback{code: code, state: state, iss: query.Get("iss")}
	}
	// Allow "code<whitespace>state" pastes for clients that strip the URL.
	fields := strings.Fields(value)
	if len(fields) == 2 {
		return oauthCallback{code: fields[0], state: fields[1]}
	}
	return oauthCallback{err: errors.New("MCP OAuth paste must be a callback URL (with code and state) or \"code state\"")}
}
