package auth

import (
	"errors"
	"os"
)

type LogoutResult struct {
	WasLoggedIn    bool
	Email          string
	APIKeyStillSet bool
	ClearedCurrent bool
	Credential     Credential
}

// Logout removes the requested OAuth scope only when the current scope exists.
func Logout(path, currentScope string, requestedScope *string) (LogoutResult, error) {
	result := LogoutResult{}
	credential, err := Load(path, currentScope)
	if errors.Is(err, os.ErrNotExist) {
		_, result.APIKeyStillSet = ReadAPIKeyEnvironment()
		return result, nil
	}
	if err != nil {
		return result, err
	}

	target := currentScope
	if requestedScope != nil {
		target = *requestedScope
	}
	if err := Remove(path, target); err != nil {
		return result, err
	}
	_, result.APIKeyStillSet = ReadAPIKeyEnvironment()
	result.WasLoggedIn = true
	result.Email = credential.Email
	result.ClearedCurrent = target == currentScope
	result.Credential = credential
	return result, nil
}
