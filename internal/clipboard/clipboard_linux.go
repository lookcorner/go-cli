package clipboard

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

func readPlatform(ctx context.Context) (Content, error) {
	type command struct {
		name string
		args []string
	}
	var imageCommands, textCommands []command
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		imageCommands = append(imageCommands, command{"wl-paste", []string{"-t", "image/png"}})
		textCommands = append(textCommands, command{"wl-paste", []string{"--no-newline", "-t", "text"}})
	}
	if os.Getenv("DISPLAY") != "" {
		imageCommands = append(imageCommands, command{"xclip", []string{"-o", "-selection", "clipboard", "-t", "image/png"}})
		textCommands = append(textCommands,
			command{"xclip", []string{"-o", "-selection", "clipboard"}},
			command{"xsel", []string{"--clipboard", "--output"}},
		)
	}
	for _, candidate := range imageCommands {
		if _, err := exec.LookPath(candidate.name); err != nil {
			continue
		}
		if output, err := exec.CommandContext(ctx, candidate.name, candidate.args...).Output(); err == nil && len(output) > 0 {
			return Content{MediaType: "image/png", Data: output}, nil
		}
	}
	for _, candidate := range textCommands {
		if _, err := exec.LookPath(candidate.name); err != nil {
			continue
		}
		if output, err := exec.CommandContext(ctx, candidate.name, candidate.args...).Output(); err == nil {
			return Content{Text: string(output)}, nil
		}
	}
	return Content{}, errors.New("no native clipboard tool is available")
}

func readPrimaryPlatform(ctx context.Context) (string, error) {
	if os.Getenv("DISPLAY") == "" {
		return "", errors.New("X11 primary selection is unavailable")
	}
	for _, candidate := range []struct {
		name string
		args []string
	}{
		{"xclip", []string{"-o", "-selection", "primary"}},
		{"xsel", []string{"--primary", "--output"}},
	} {
		if _, err := exec.LookPath(candidate.name); err != nil {
			continue
		}
		if output, err := exec.CommandContext(ctx, candidate.name, candidate.args...).Output(); err == nil {
			return string(output), nil
		}
	}
	return "", errors.New("no X11 primary selection tool is available")
}
