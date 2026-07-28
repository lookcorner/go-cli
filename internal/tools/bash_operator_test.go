package tools

import "testing"

func TestContainsUnwaitedBackgroundOperator(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{"sleep 60 &", true},
		{"sleep 60 & echo done", true},
		{"echo a && echo b", false},
		{"cmd 2>&1 &", true},
		{"cmd 2>&1", false},
		{"cmd &>/dev/null", false},
		{`echo "a & b"`, false},
		{`echo a \& b`, false},
		{"cmd1 & cmd2 & wait", false},
		{"cmd1 & cmd2 & wait; ", false},
		{"sleep 60 & await", true},
		{"cat <<'EOF'\n& inside body\nEOF\necho done", false},
		{"cat <<- EOF\n\t& inside body\n\tEOF\nsleep 60 &", true},
		{"cat <<< '&'", false},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			if got := containsUnwaitedBackgroundOperator(test.command); got != test.want {
				t.Fatalf("containsUnwaitedBackgroundOperator(%q)=%v want %v", test.command, got, test.want)
			}
		})
	}
}
