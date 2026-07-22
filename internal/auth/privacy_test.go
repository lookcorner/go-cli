package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetCodingDataRetentionPreservesCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"scope":{"key":"fresh","refresh_token":"refresh","unknown":"keep","coding_data_retention_opt_out":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetCodingDataRetention(path, "scope", true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"key": "fresh"`) || !strings.Contains(string(data), `"refresh_token": "refresh"`) || !strings.Contains(string(data), `"unknown": "keep"`) || !strings.Contains(string(data), `"coding_data_retention_opt_out": true`) {
		t.Fatalf("updated credential=%s err=%v", data, err)
	}
}

func TestSetCodingDataRetentionRequiresScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := Save(path, "other", Credential{Key: "token"}); err != nil {
		t.Fatal(err)
	}
	if err := SetCodingDataRetention(path, "missing", true); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing scope error=%v", err)
	}
	if err := os.WriteFile(path, []byte(`{"scope":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetCodingDataRetention(path, "scope", true); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("null scope error=%v", err)
	}
}
