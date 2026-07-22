package auth

import (
	"os"
	"time"
)

const APIKeyScope = "xai::api_key"

func ReadAPIKeyEnvironment() (string, bool) {
	if key, ok := os.LookupEnv("XAI_API_KEY"); ok {
		return key, true
	}
	return os.LookupEnv("GROK_CODE_XAI_API_KEY")
}

func StoreAPIKey(path, key string) error {
	if key == "" {
		return Remove(path, APIKeyScope)
	}
	return Save(path, APIKeyScope, Credential{
		Key: key, AuthMode: "api_key", CreateTime: time.Now().UTC(),
	})
}
