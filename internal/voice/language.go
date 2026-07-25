package voice

import (
	"os"
	"strings"
)

var Languages = [...]string{
	"en", "auto", "ar", "cs", "da", "nl", "fil", "fr", "de", "hi", "id", "it", "ja",
	"ko", "mk", "ms", "fa", "pl", "pt", "ro", "ru", "es", "sv", "th", "tr", "vi",
}

func CanonicalLanguage(value string) string {
	raw := strings.TrimSpace(value)
	if strings.EqualFold(raw, "auto") {
		return "auto"
	}
	primary := strings.FieldsFunc(raw, func(r rune) bool { return r == '_' || r == '-' || r == '.' })
	if len(primary) == 0 {
		return "en"
	}
	code := strings.ToLower(primary[0])
	if code == "tl" {
		return "fil"
	}
	for _, supported := range Languages {
		if supported == code && supported != "auto" {
			return supported
		}
	}
	return "en"
}

func LanguageForAPI(value string) string {
	canonical := CanonicalLanguage(value)
	if canonical != "auto" {
		return canonical
	}
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if locale := strings.TrimSpace(os.Getenv(name)); locale != "" {
			if strings.EqualFold(locale, "C") || strings.EqualFold(locale, "POSIX") {
				return "en"
			}
			return CanonicalLanguage(locale)
		}
	}
	return "en"
}
