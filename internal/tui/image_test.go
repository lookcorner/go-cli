package tui

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestDetectImageProtocol(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		environ map[string]string
		want    imageProtocol
	}{
		"kitty env":      {map[string]string{"KITTY_WINDOW_ID": "1"}, imageProtocolKitty},
		"kitty term":     {map[string]string{"TERM": "xterm-kitty"}, imageProtocolKitty},
		"ghostty":        {map[string]string{"GHOSTTY_RESOURCES_DIR": "/opt/ghostty"}, imageProtocolKitty},
		"wezterm":        {map[string]string{"WEZTERM_EXECUTABLE": "/opt/wezterm"}, imageProtocolKitty},
		"iterm session":  {map[string]string{"ITERM_SESSION_ID": "w0t0p0"}, imageProtocolITerm2},
		"iterm program":  {map[string]string{"TERM_PROGRAM": "iTerm.app"}, imageProtocolITerm2},
		"mintty":         {map[string]string{"TERM_PROGRAM": "mintty"}, imageProtocolITerm2},
		"windows term":   {map[string]string{"WT_SESSION": "abc"}, imageProtocolSixel},
		"sixel term":     {map[string]string{"TERM": "foot-sixel"}, imageProtocolSixel},
		"kitty wins":     {map[string]string{"KITTY_WINDOW_ID": "1", "WT_SESSION": "abc"}, imageProtocolKitty},
		"plain terminal": {map[string]string{"TERM": "xterm-256color"}, imageProtocolNone},
		"empty":          {map[string]string{}, imageProtocolNone},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			got := detectImageProtocol(func(key string) string { return test.environ[key] })
			if got != test.want {
				t.Fatalf("detectImageProtocol(%v) = %v, want %v", test.environ, got, test.want)
			}
		})
	}
}

func TestKittyImageSequenceChunksWithContinuationFlags(t *testing.T) {
	t.Parallel()
	tiny := kittyImageSequence([]byte("png-bytes"))
	if string(tiny) != "\x1b_Ga=T,f=100,m=0;"+base64.StdEncoding.EncodeToString([]byte("png-bytes"))+"\x1b\\" {
		t.Fatalf("tiny sequence=%q", tiny)
	}
	large := kittyImageSequence(make([]byte, 6000))
	text := string(large)
	if !strings.HasPrefix(text, "\x1b_Ga=T,f=100,m=1;") {
		t.Fatalf("first chunk missing continuation: %.40q", text)
	}
	if !strings.HasSuffix(text, "\x1b\\") {
		t.Fatal("sequence not terminated")
	}
	parts := strings.Split(text, "\x1b\\")
	if parts[len(parts)-1] != "" {
		t.Fatal("trailing content after final chunk")
	}
	var decoded strings.Builder
	for index, part := range parts[:len(parts)-1] {
		headerEnd := strings.Index(part, ";")
		if headerEnd < 0 {
			t.Fatalf("chunk %d missing header: %.40q", index, part)
		}
		header := part[:headerEnd]
		if index < len(parts)-2 && !strings.HasSuffix(header, "m=1") {
			t.Fatalf("chunk %d lost continuation flag: %q", index, header)
		}
		if index == len(parts)-2 && !strings.HasSuffix(header, "m=0") {
			t.Fatalf("final chunk keeps continuation flag: %q", header)
		}
		payload := part[headerEnd+1:]
		if len(payload) > 4096 {
			t.Fatalf("chunk %d exceeds 4096 chars: %d", index, len(payload))
		}
		decoded.WriteString(payload)
	}
	if decoded.String() != base64.StdEncoding.EncodeToString(make([]byte, 6000)) {
		t.Fatal("chunked payload does not reassemble to the original data")
	}
}

func TestITermImageSequence(t *testing.T) {
	t.Parallel()
	sequence := string(itermImageSequence([]byte("img"), 12, 4))
	want := "\x1b]1337;File=inline=1;preserveAspectRatio=1;width=12;height=4:" +
		base64.StdEncoding.EncodeToString([]byte("img")) + "\a"
	if sequence != want {
		t.Fatalf("sequence=%q want=%q", sequence, want)
	}
	unbounded := string(itermImageSequence([]byte("img"), 0, 0))
	if strings.Contains(unbounded, "width=") || strings.Contains(unbounded, "height=") {
		t.Fatalf("unbounded sequence carried bounds: %q", unbounded)
	}
}
