package clipboard

import (
	"context"
	"encoding/base64"
	"errors"
	"os/exec"
	"strings"
)

func readPlatform(ctx context.Context) (Content, error) {
	script := `$ErrorActionPreference='Stop'; Add-Type -AssemblyName System.Windows.Forms; ` +
		`$i=[Windows.Forms.Clipboard]::GetImage(); if ($null -ne $i) { ` +
		`$m=New-Object IO.MemoryStream; $i.Save($m,[Drawing.Imaging.ImageFormat]::Png); ` +
		`[Convert]::ToBase64String($m.ToArray()) } elseif ([Windows.Forms.Clipboard]::ContainsText()) { ` +
		`[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes([Windows.Forms.Clipboard]::GetText())) }`
	output, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return Content{}, errors.New("clipboard is unavailable")
	}
	encoded := strings.TrimSpace(string(output))
	if encoded == "" {
		return Content{}, nil
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return Content{}, errors.New("clipboard returned invalid data")
	}
	if len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n" {
		return Content{MediaType: "image/png", Data: data}, nil
	}
	return Content{Text: string(data)}, nil
}

func readPrimaryPlatform(context.Context) (string, error) {
	return "", errors.New("primary selection is unavailable on this platform")
}
