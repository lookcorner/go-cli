package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func TestBrowserLoginPKCEAndIDTokenValidation(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: privateKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key"),
	)
	if err != nil {
		t.Fatal(err)
	}
	var issuer, expectedNonce string
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/jwks":
			_ = json.NewEncoder(writer).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
				Key: &privateKey.PublicKey, KeyID: "test-key", Algorithm: string(jose.RS256), Use: "sig",
			}}})
		case "/token":
			if err := request.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if request.Form.Get("grant_type") != "authorization_code" || request.Form.Get("code") != "auth-code" || request.Form.Get("code_verifier") == "" {
				t.Fatalf("unexpected token form: %#v", request.Form)
			}
			now := time.Now()
			idToken, err := jwt.Signed(signer).Claims(map[string]any{
				"iss": issuer, "aud": "client-1", "sub": "user-1", "email": "user@example.com",
				"nonce": expectedNonce, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
			}).Serialize()
			if err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 3600,
				"token_type": "Bearer", "id_token": idToken,
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	issuer = "http://" + server.Listener.Addr().String()
	server.Start()
	defer server.Close()

	client := NewClient(server.Client())
	login, err := client.StartBrowserLogin(context.Background(), Config{
		Issuer: issuer, ClientID: "client-1", Scopes: []string{"openid", "email"}, Audience: "api-audience",
	})
	if err != nil {
		t.Fatal(err)
	}
	authorizeURL, err := url.Parse(login.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	query := authorizeURL.Query()
	expectedNonce = query.Get("nonce")
	if authorizeURL.Path != "/authorize" || query.Get("response_type") != "code" || query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" || query.Get("state") == "" || expectedNonce == "" || query.Get("referrer") != "grok-build" || query.Get("audience") != "api-audience" {
		t.Fatalf("authorization URL=%s", login.AuthorizationURL)
	}
	callbackURL := query.Get("redirect_uri") + "?code=auth-code&state=" + url.QueryEscape(query.Get("state"))
	go func() {
		response, err := http.Get(callbackURL)
		if err == nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	credential, err := login.Complete(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if credential.Key != "access-1" || credential.RefreshToken != "refresh-1" || credential.UserID != "user-1" || credential.Email != "user@example.com" || credential.TokenEndpoint != issuer+"/token" || credential.ExpiresAt == nil {
		t.Fatalf("credential=%#v", credential)
	}
}

func TestBrowserLoginRejectsMismatchedState(t *testing.T) {
	issuer := ""
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]string{
			"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
			"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
		})
	}))
	issuer = "http://" + server.Listener.Addr().String()
	server.Start()
	defer server.Close()
	login, err := NewClient(server.Client()).StartBrowserLogin(context.Background(), Config{
		Issuer: issuer, ClientID: "client-1", Scopes: []string{"openid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	authorizeURL, _ := url.Parse(login.AuthorizationURL)
	go http.Get(authorizeURL.Query().Get("redirect_uri") + "?code=auth-code&state=wrong")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := login.Complete(ctx, nil); err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("unexpected state error: %v", err)
	}
}

func TestParsePastedOIDCCallback(t *testing.T) {
	callback := parseCallback("https://localhost/callback?code=abc&state=state-1")
	if callback.err != nil || callback.code != "abc" || callback.state != "state-1" {
		t.Fatalf("callback=%#v", callback)
	}
	if bare := parseCallback("bare-code"); bare.err != nil || bare.code != "bare-code" || bare.state != "" {
		t.Fatalf("bare callback=%#v", bare)
	}
	denied := parseCallback("https://localhost/callback?error=access_denied&error_description=no")
	if denied.err == nil || !strings.Contains(denied.err.Error(), "access_denied: no") {
		t.Fatalf("denied callback=%#v", denied)
	}
}
