package mcp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
)

type authFlight struct {
	done   chan struct{}
	result AuthenticateResult
	err    error
	gen    uint64
}

var (
	authFlightsMu sync.Mutex
	authFlights   = map[string]*authFlight{}
	authFlightGen atomic.Uint64
)

// AuthenticateMCPServer runs browser PKCE enrollment with in-process and
// cross-process dedup so concurrent callers share one browser tab.
func AuthenticateMCPServer(ctx context.Context, name, serverURL string, opts AuthenticateOpts) (AuthenticateResult, error) {
	name = strings.TrimSpace(name)
	serverURL = strings.TrimSpace(serverURL)
	if name == "" || serverURL == "" {
		return AuthenticateResult{}, errors.New("MCP server name and URL are required")
	}
	key := CredentialKey(name, serverURL)

	if !opts.Force {
		authFlightsMu.Lock()
		if flight, ok := authFlights[key]; ok {
			authFlightsMu.Unlock()
			select {
			case <-flight.done:
				return flight.result, flight.err
			case <-ctx.Done():
				return AuthenticateResult{}, ctx.Err()
			}
		}
		flight := &authFlight{done: make(chan struct{}), gen: authFlightGen.Add(1)}
		authFlights[key] = flight
		authFlightsMu.Unlock()

		result, err := authenticateWithAuthLock(ctx, name, serverURL, opts)
		finishAuthFlight(key, flight, result, err)
		return result, err
	}

	authFlightsMu.Lock()
	delete(authFlights, key)
	flight := &authFlight{done: make(chan struct{}), gen: authFlightGen.Add(1)}
	authFlights[key] = flight
	authFlightsMu.Unlock()

	result, err := authenticateMCPServerFlow(ctx, name, serverURL, opts)
	finishAuthFlight(key, flight, result, err)
	return result, err
}

func finishAuthFlight(key string, flight *authFlight, result AuthenticateResult, err error) {
	authFlightsMu.Lock()
	defer authFlightsMu.Unlock()
	if current, ok := authFlights[key]; ok && current.gen == flight.gen {
		flight.result, flight.err = result, err
		delete(authFlights, key)
		close(flight.done)
	}
}

func authenticateWithAuthLock(ctx context.Context, name, serverURL string, opts AuthenticateOpts) (AuthenticateResult, error) {
	path := strings.TrimSpace(opts.CredentialsPath)
	if path == "" {
		var err error
		path, err = DefaultCredentialsPath()
		if err != nil {
			return AuthenticateResult{}, err
		}
		opts.CredentialsPath = path
	}
	tokenBefore := storedAccessToken(path, name, serverURL)
	release, err := acquireMCPAuthFileLock(ctx, name)
	if err != nil {
		// Fail open: proceed without cross-process dedup.
		return authenticateMCPServerFlow(ctx, name, serverURL, opts)
	}
	if release != nil {
		defer release()
	}
	if token := storedAccessToken(path, name, serverURL); token != "" && token != tokenBefore {
		if store, loadErr := LoadCredentialStore(path); loadErr == nil {
			if creds, ok := store.Get(name, serverURL); ok && creds.AccessToken() != "" {
				return AuthenticateResult{Credentials: creds}, nil
			}
		}
	}
	return authenticateMCPServerFlow(ctx, name, serverURL, opts)
}

func storedAccessToken(path, name, serverURL string) string {
	store, err := LoadCredentialStore(path)
	if err != nil {
		return ""
	}
	creds, ok := store.Get(name, serverURL)
	if !ok {
		return ""
	}
	return creds.AccessToken()
}
