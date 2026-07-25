package clipboard

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
)

func readPlatform(ctx context.Context) (Content, error) {
	file, err := os.CreateTemp("", "gork-clipboard-*.png")
	if err != nil {
		return Content{}, err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		os.Remove(path)
		return Content{}, err
	}
	defer os.Remove(path)

	script := `on run argv
try
set imageData to the clipboard as «class PNGf»
set outputFile to open for access POSIX file (item 1 of argv) with write permission
set eof outputFile to 0
write imageData to outputFile
close access outputFile
return "ok"
on error
try
close access outputFile
end try
return ""
end try
end run`
	if output, runErr := exec.CommandContext(ctx, "osascript", "-e", script, "--", path).Output(); runErr == nil && strings.TrimSpace(string(output)) == "ok" {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return Content{}, readErr
		}
		if len(data) > 0 {
			return Content{MediaType: "image/png", Data: data}, nil
		}
	}
	output, err := exec.CommandContext(ctx, "pbpaste").Output()
	if err != nil {
		return Content{}, errors.New("clipboard is unavailable")
	}
	return Content{Text: string(output)}, nil
}
