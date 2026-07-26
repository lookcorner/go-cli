package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// OAuthMetadata is the subset of RFC 8414 metadata required for MCP enrollment.
type OAuthMetadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint,omitempty"`
	ScopesSupported       []string `json:"scopes_supported,omitempty"`
}

type protectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported,omitempty"`
	AuthorizationServerURL string   `json:"authorization_server,omitempty"`
}

// DiscoverOAuthMetadata resolves AS metadata for an MCP server URL via
// RFC 9728 protected-resource well-knowns, then RFC 8414 AS metadata.
// Falls back to AS discovery on the MCP origin when PRM is absent.
func DiscoverOAuthMetadata(ctx context.Context, serverURL string, client *http.Client) (OAuthMetadata, error) {
	if client == nil {
		client = http.DefaultClient
	}
	parsed, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return OAuthMetadata{}, fmt.Errorf("invalid MCP URL %q", serverURL)
	}
	origin := &url.URL{Scheme: parsed.Scheme, Host: parsed.Host}

	var asURL string
	var scopes []string
	for _, candidate := range protectedResourceCandidates(origin, parsed) {
		prm, ok, err := fetchProtectedResource(ctx, client, candidate)
		if err != nil {
			return OAuthMetadata{}, err
		}
		if !ok {
			continue
		}
		scopes = prm.ScopesSupported
		if len(prm.AuthorizationServers) > 0 {
			asURL = strings.TrimSpace(prm.AuthorizationServers[0])
			break
		}
		if as := strings.TrimSpace(prm.AuthorizationServerURL); as != "" {
			asURL = as
			break
		}
	}
	if asURL == "" {
		asURL = origin.String()
	}
	meta, err := fetchAuthorizationServerMetadata(ctx, client, asURL)
	if err != nil {
		return OAuthMetadata{}, err
	}
	if len(meta.ScopesSupported) == 0 && len(scopes) > 0 {
		meta.ScopesSupported = scopes
	}
	if strings.TrimSpace(meta.AuthorizationEndpoint) == "" || strings.TrimSpace(meta.TokenEndpoint) == "" {
		return OAuthMetadata{}, fmt.Errorf("OAuth metadata missing authorization or token endpoint for %q", asURL)
	}
	return meta, nil
}

func protectedResourceCandidates(origin, server *url.URL) []string {
	path := strings.TrimSuffix(server.EscapedPath(), "/")
	candidates := make([]string, 0, 3)
	if path != "" && path != "/" {
		candidates = append(candidates, origin.String()+"/.well-known/oauth-protected-resource"+path)
	}
	candidates = append(candidates, origin.String()+"/.well-known/oauth-protected-resource")
	return candidates
}

func fetchProtectedResource(ctx context.Context, client *http.Client, rawURL string) (protectedResourceMetadata, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return protectedResourceMetadata{}, false, err
	}
	response, err := client.Do(request)
	if err != nil {
		return protectedResourceMetadata{}, false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return protectedResourceMetadata{}, false, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return protectedResourceMetadata{}, false, nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return protectedResourceMetadata{}, false, err
	}
	var prm protectedResourceMetadata
	if err := json.Unmarshal(body, &prm); err != nil {
		return protectedResourceMetadata{}, false, nil
	}
	return prm, true, nil
}

func fetchAuthorizationServerMetadata(ctx context.Context, client *http.Client, asURL string) (OAuthMetadata, error) {
	parsed, err := url.Parse(strings.TrimSpace(asURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return OAuthMetadata{}, fmt.Errorf("invalid authorization server URL %q", asURL)
	}
	origin := &url.URL{Scheme: parsed.Scheme, Host: parsed.Host}
	path := strings.TrimSuffix(parsed.EscapedPath(), "/")
	candidates := []string{origin.String() + "/.well-known/oauth-authorization-server"}
	if path != "" && path != "/" {
		candidates = append([]string{
			origin.String() + "/.well-known/oauth-authorization-server" + path,
			strings.TrimRight(asURL, "/") + "/.well-known/oauth-authorization-server",
		}, candidates...)
	}
	var lastErr error
	for _, candidate := range candidates {
		meta, ok, err := getJSONMetadata(ctx, client, candidate)
		if err != nil {
			lastErr = err
			continue
		}
		if ok {
			if meta.Issuer == "" {
				meta.Issuer = strings.TrimRight(asURL, "/")
			}
			return meta, nil
		}
	}
	if lastErr != nil {
		return OAuthMetadata{}, lastErr
	}
	return OAuthMetadata{}, fmt.Errorf("OAuth authorization server metadata not found for %q", asURL)
}

func getJSONMetadata(ctx context.Context, client *http.Client, rawURL string) (OAuthMetadata, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return OAuthMetadata{}, false, err
	}
	response, err := client.Do(request)
	if err != nil {
		return OAuthMetadata{}, false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return OAuthMetadata{}, false, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return OAuthMetadata{}, false, fmt.Errorf("OAuth metadata %s returned %s", rawURL, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return OAuthMetadata{}, false, err
	}
	var meta OAuthMetadata
	if err := json.Unmarshal(body, &meta); err != nil {
		return OAuthMetadata{}, false, fmt.Errorf("decode OAuth metadata: %w", err)
	}
	return meta, true, nil
}
