//go:build windows

package wrap

import "testing"

func TestEscapeArg(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{`foo`, `foo`},
		{`a\b`, `a\b`},
		{``, `""`},
		{`a b`, `"a b"`},
		{"a\tb", `"a	b"`},
		{`a"b`, `"a\"b"`},
		{`C:\Program Files\app`, `"C:\Program Files\app"`},
		{`a b\`, `"a b\\"`},
		{`a\"b`, `"a\\\"b"`},
	}
	for _, tc := range cases {
		if got := escapeArg(tc.in); got != tc.want {
			t.Errorf("escapeArg(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestJoinCommandLine(t *testing.T) {
	t.Parallel()
	got := joinCommandLine(`C:\Program Files\app.exe`, []string{`/a`, `x y`})
	want := `"C:\Program Files\app.exe" /a "x y"`
	if got != want {
		t.Errorf("joinCommandLine = %q, want %q", got, want)
	}
}
