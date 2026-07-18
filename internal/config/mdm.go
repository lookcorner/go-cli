package config

import (
	"encoding/base64"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
)

const mdmRequirementsSource = "ai.x.grok:requirements_toml_base64"

func decodeManagedRequirements(encoded string) []byte {
	compact := strings.Map(func(char rune) rune {
		if char == ' ' || char == '\t' || char == '\r' || char == '\n' {
			return -1
		}
		return char
	}, encoded)
	data, err := base64.StdEncoding.DecodeString(compact)
	if err != nil || !utf8.Valid(data) {
		return nil
	}
	var table map[string]any
	if toml.Unmarshal(data, &table) != nil || len(table) == 0 {
		return nil
	}
	return data
}
