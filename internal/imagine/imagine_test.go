package imagine

import (
	"strings"
	"testing"
)

func TestParseImageAndVideoCommands(t *testing.T) {
	image, ok := Parse("  /imagine   a golden sunset  ")
	if !ok || image.Display != "/imagine a golden sunset" || image.RequiredTool != ImageTool || !strings.Contains(image.Instruction, "verbatim") || !strings.HasSuffix(image.Instruction, "Prompt: a golden sunset") {
		t.Fatalf("image command=%#v ok=%v", image, ok)
	}
	video, ok := Parse("/imagine-video a camera orbit")
	if !ok || video.Display != "/imagine-video a camera orbit" || video.RequiredTool != VideoTool || !strings.Contains(video.Instruction, "image_to_video") || !strings.Contains(video.Instruction, "reference_to_video") || !strings.HasSuffix(video.Instruction, "User prompt: a camera orbit") {
		t.Fatalf("video command=%#v ok=%v", video, ok)
	}
}

func TestParseUsageAndExactCommandNames(t *testing.T) {
	for _, input := range []string{"/imagine", "/imagine   ", "/imagine-video"} {
		command, ok := Parse(input)
		if !ok || command.Instruction != "" || !strings.HasPrefix(command.Usage, "Usage: ") {
			t.Fatalf("input=%q command=%#v ok=%v", input, command, ok)
		}
	}
	for _, input := range []string{"", "hello", "/imagines cat", "/imagine-videos cat"} {
		if command, ok := Parse(input); ok {
			t.Fatalf("input=%q unexpectedly parsed as %#v", input, command)
		}
	}
}
