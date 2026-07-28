package tools

import "testing"

func TestFindSelfMatchingProcessKill(t *testing.T) {
	tests := []struct {
		command     string
		wantCommand string
		wantPattern string
	}{
		{"pkill -f ./server && sleep 0.5 && ./server > log 2>&1", "pkill", "./server"},
		{"pkill -f './server' && nohup ./server", "pkill", "./server"},
		{`pkill -f "myserver"; ./myserver`, "pkill", "myserver"},
		{"pgrep -f ./server | xargs -r kill ; ./server &", "pgrep", "./server"},
		{"pkill -fe ./server && ./server", "pkill", "./server"},
		{"pkill --full ./server && ./server", "pkill", "./server"},
		{"pkill -f ./server", "", ""},
		{"pkill -x server && ./server", "", ""},
		{"pkill -f ./other && ./server", "", ""},
		{`pkill -f "$(cat pattern)" && ./server`, "", ""},
		{"xpkill -f ./server && ./server", "", ""},
		{"pgrep -f ./server && echo found", "", ""},
		{"pkill -f a && echo a", "", ""},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			hit, found := findSelfMatchingProcessKill(test.command)
			if found != (test.wantCommand != "") || hit.command != test.wantCommand || hit.pattern != test.wantPattern {
				t.Fatalf("hit=%#v found=%v want command=%q pattern=%q", hit, found, test.wantCommand, test.wantPattern)
			}
		})
	}
}
