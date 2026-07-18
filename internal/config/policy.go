package config

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const (
	policySignatureFile = "managed_config.sig.json"
	policyMarkerFile    = "managed_config.sync.json"
)

type PolicyKey struct {
	ID  string
	Key ed25519.PublicKey
}

// Provision keys here at build time to turn signature enforcement on.
var embeddedPolicyKeys []PolicyKey

type PolicyClient struct {
	HTTP *http.Client
	keys []PolicyKey
	now  func() time.Time
}

type policyResponse struct {
	DeploymentID string              `json:"deployment_id"`
	TeamID       string              `json:"team_id"`
	Managed      *string             `json:"managed_config"`
	Requirements *string             `json:"requirements"`
	Signatures   []signatureEnvelope `json:"signatures"`
}

type signatureEnvelope struct {
	SignedPayload string `json:"signed_payload"`
	Signature     string `json:"signature"`
	KeyID         string `json:"key_id"`
}

type signedPayload struct {
	Version      uint64  `json:"version"`
	DeploymentID *string `json:"deployment_id"`
	TeamID       *string `json:"team_id"`
	Managed      *string `json:"managed_config"`
	Requirements *string `json:"requirements"`
	FailClosed   bool    `json:"fail_closed"`
	ExpiresAt    uint64  `json:"expires_at"`
	KeyID        string  `json:"key_id"`
}

type policyMarker struct {
	Principal       string `json:"principal,omitempty"`
	KeyFingerprint  string `json:"key_fingerprint,omitempty"`
	FailClosed      bool   `json:"fail_closed"`
	HadManaged      bool   `json:"had_managed_config"`
	HadRequirements bool   `json:"had_requirements"`
}

func NewPolicyClient(httpClient *http.Client) *PolicyClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	copy := *httpClient
	previousRedirect := copy.CheckRedirect
	copy.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > 0 && (request.URL.Scheme != via[0].URL.Scheme || !strings.EqualFold(request.URL.Host, via[0].URL.Host)) {
			return http.ErrUseLastResponse
		}
		if previousRedirect != nil {
			return previousRedirect(request, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 managed policy redirects")
		}
		return nil
	}
	return &PolicyClient{HTTP: &copy, keys: embeddedPolicyKeys, now: time.Now}
}

func (c *PolicyClient) Sync(ctx context.Context, home, endpoint, token, expectedTeam, keyFingerprint string) (bool, error) {
	if token == "" {
		return false, errors.New("managed policy requires a deployment key or team credential")
	}
	if err := validatePolicyURL(endpoint); err != nil {
		return false, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := c.HTTP.Do(request)
	if err != nil {
		return false, fmt.Errorf("fetch managed policy: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return false, errors.New("managed policy credential was rejected")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false, fmt.Errorf("fetch managed policy: server returned %s", response.Status)
	}
	var body policyResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&body); err != nil {
		return false, fmt.Errorf("decode managed policy: %w", err)
	}
	marker := policyMarker{
		KeyFingerprint:  keyFingerprint,
		HadManaged:      body.Managed != nil && *body.Managed != "",
		HadRequirements: body.Requirements != nil && *body.Requirements != "",
	}
	var sidecar *signatureEnvelope
	if len(c.keys) > 0 {
		envelope, payload, err := verifyPolicyResponse(body, c.keys, expectedTeam, uint64(c.now().Unix()))
		if err != nil {
			return false, fmt.Errorf("verify managed policy: %w", err)
		}
		sidecar = &envelope
		marker.Principal, marker.FailClosed = policyPrincipal(payload), payload.FailClosed
	}
	lock, err := acquirePolicyLock(ctx, home)
	if err != nil {
		return false, err
	}
	defer lock()
	if err := os.MkdirAll(home, 0o700); err != nil {
		return false, err
	}
	changed := false
	for name, content := range map[string]*string{"managed_config.toml": body.Managed, "requirements.toml": body.Requirements} {
		updated, err := convergePolicyFile(filepath.Join(home, name), content)
		if err != nil {
			return false, err
		}
		changed = changed || updated
	}
	if sidecar != nil {
		if err := writeJSONAtomic(filepath.Join(home, policySignatureFile), sidecar); err != nil {
			return false, err
		}
	} else if err := removePolicyPath(filepath.Join(home, policySignatureFile)); err != nil {
		return false, err
	}
	if marker.Principal == "" {
		if body.DeploymentID != "" {
			marker.Principal = body.DeploymentID
		} else {
			marker.Principal = body.TeamID
		}
		marker.FailClosed = requirementsFailClosed(body.Requirements)
	}
	if err := writeJSONAtomic(filepath.Join(home, policyMarkerFile), marker); err != nil {
		return false, err
	}
	return changed, nil
}

func VerifyManagedPolicy(home, expectedPrincipal, keyFingerprint string) error {
	return verifyManagedPolicy(home, expectedPrincipal, keyFingerprint, embeddedPolicyKeys, uint64(time.Now().Unix()))
}

func verifyManagedPolicy(home, expectedPrincipal, keyFingerprint string, keys []PolicyKey, now uint64) error {
	if len(keys) == 0 {
		return nil
	}
	marker, _ := readPolicyMarker(home)
	if expectedPrincipal == "" && keyFingerprint != "" && marker.KeyFingerprint == keyFingerprint {
		expectedPrincipal = marker.Principal
	}
	data, err := readRegularFile(filepath.Join(home, policySignatureFile))
	if err != nil {
		if marker.FailClosed && (marker.HadManaged || marker.HadRequirements || policyFilesExist(home)) {
			return errors.New("managed policy signature is missing or unreadable")
		}
		return nil
	}
	var envelope signatureEnvelope
	if json.Unmarshal(data, &envelope) != nil {
		if marker.FailClosed {
			return errors.New("managed policy signature is invalid")
		}
		return nil
	}
	payload, err := verifySignedPayload(envelope, keys)
	if err != nil {
		if marker.FailClosed {
			return fmt.Errorf("managed policy signature is invalid: %w", err)
		}
		return nil
	}
	if expectedPrincipal != "" && policyPrincipal(payload) != "" && policyPrincipal(payload) != expectedPrincipal {
		return errors.New("managed policy is bound to a different principal")
	}
	if !payload.FailClosed {
		return nil
	}
	if now > payload.ExpiresAt {
		return errors.New("managed policy has expired")
	}
	if err := checkPolicyFiles(home, payload); err != nil {
		return err
	}
	return nil
}

func verifyPolicyResponse(body policyResponse, keys []PolicyKey, expectedTeam string, now uint64) (signatureEnvelope, signedPayload, error) {
	if len(body.Signatures) == 0 {
		return signatureEnvelope{}, signedPayload{}, errors.New("server returned no signature")
	}
	envelope := body.Signatures[0]
	for _, candidate := range body.Signatures {
		if findPolicyKey(keys, candidate.KeyID) != nil {
			envelope = candidate
			break
		}
	}
	payload, err := verifySignedPayload(envelope, keys)
	if err != nil {
		return signatureEnvelope{}, signedPayload{}, err
	}
	if now > payload.ExpiresAt {
		return signatureEnvelope{}, signedPayload{}, errors.New("signed policy has expired")
	}
	if payload.DeploymentID == nil && expectedTeam != "" && stringValue(payload.TeamID) != expectedTeam {
		return signatureEnvelope{}, signedPayload{}, errors.New("signed policy is bound to a different team")
	}
	if !equalOptional(body.Managed, payload.Managed) || !equalOptional(body.Requirements, payload.Requirements) {
		return signatureEnvelope{}, signedPayload{}, errors.New("served policy does not match signed payload")
	}
	return envelope, payload, nil
}

func verifySignedPayload(envelope signatureEnvelope, keys []PolicyKey) (signedPayload, error) {
	var payload signedPayload
	if json.Unmarshal([]byte(envelope.SignedPayload), &payload) != nil {
		return payload, errors.New("signed payload is not valid JSON")
	}
	key := findPolicyKey(keys, payload.KeyID)
	if key == nil {
		return payload, errors.New("signed payload uses an unknown key")
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(envelope.Signature))
	if err != nil || !ed25519.Verify(key, []byte(envelope.SignedPayload), signature) {
		return payload, errors.New("signature does not verify")
	}
	return payload, nil
}

func checkPolicyFiles(home string, payload signedPayload) error {
	for name, expected := range map[string]*string{"managed_config.toml": payload.Managed, "requirements.toml": payload.Requirements} {
		data, err := readRegularFile(filepath.Join(home, name))
		if expected == nil || *expected == "" {
			if errors.Is(err, os.ErrNotExist) || err == nil && len(data) == 0 {
				continue
			}
			return fmt.Errorf("on-disk %s does not match signed policy", name)
		}
		if err != nil || string(data) != *expected {
			return fmt.Errorf("on-disk %s does not match signed policy", name)
		}
	}
	return nil
}

func convergePolicyFile(path string, content *string) (bool, error) {
	if content == nil || *content == "" {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return true, removePolicyPath(path)
	}
	if current, err := readRegularFile(path); err == nil && string(current) == *content {
		return false, nil
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		if err := removePolicyPath(path); err != nil {
			return false, err
		}
	}
	return true, writeAtomic(path, []byte(*content))
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".managed-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func removePolicyPath(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

func readRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("policy path is not a regular file")
	}
	return os.ReadFile(path)
}

func acquirePolicyLock(ctx context.Context, home string) (func(), error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(home, "managed_config.lock")
	for {
		if err := os.Mkdir(path, 0o700); err == nil {
			return func() { _ = os.Remove(path) }, nil
		} else if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) > 2*time.Minute {
			_ = os.Remove(path)
			continue
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func validatePolicyURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return errors.New("managed policy URL is invalid")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || net.ParseIP(parsed.Hostname()).IsLoopback()) {
		return nil
	}
	return errors.New("managed policy URL must use HTTPS")
}

func PolicyHome() (string, error) {
	path, err := DefaultPath()
	if err != nil {
		return "", err
	}
	return filepath.Dir(path), nil
}

func ClearManagedPolicy(ctx context.Context, home string) error {
	lock, err := acquirePolicyLock(ctx, home)
	if err != nil {
		return err
	}
	defer lock()
	for _, name := range []string{"managed_config.toml", "requirements.toml", policySignatureFile, policyMarkerFile} {
		if err := removePolicyPath(filepath.Join(home, name)); err != nil {
			return err
		}
	}
	return nil
}

func DeploymentKeyFingerprint(key string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:])
}

func findPolicyKey(keys []PolicyKey, id string) ed25519.PublicKey {
	for _, key := range keys {
		if key.ID == id && len(key.Key) == ed25519.PublicKeySize {
			return key.Key
		}
	}
	return nil
}

func policyPrincipal(payload signedPayload) string {
	if payload.DeploymentID != nil {
		return *payload.DeploymentID
	}
	return stringValue(payload.TeamID)
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func equalOptional(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func requirementsFailClosed(value *string) bool {
	if value == nil {
		return false
	}
	var raw map[string]any
	return toml.Unmarshal([]byte(*value), &raw) == nil && raw["fail_closed"] == true
}

func policyFilesExist(home string) bool {
	for _, name := range []string{"managed_config.toml", "requirements.toml"} {
		if _, err := os.Lstat(filepath.Join(home, name)); err == nil {
			return true
		}
	}
	return false
}

func readPolicyMarker(home string) (policyMarker, error) {
	data, err := readRegularFile(filepath.Join(home, policyMarkerFile))
	if err != nil {
		return policyMarker{}, err
	}
	var marker policyMarker
	err = json.Unmarshal(data, &marker)
	return marker, err
}
