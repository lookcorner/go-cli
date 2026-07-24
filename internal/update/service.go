package update

import "fmt"

const ReleasesURL = "https://github.com/thedavidweng/gork-build/releases"

type Status struct {
	CurrentVersion  string  `json:"currentVersion"`
	LatestVersion   *string `json:"latestVersion"`
	UpdateAvailable bool    `json:"updateAvailable"`
	Installer       *string `json:"installer"`
	Channel         string  `json:"channel"`
	AutoUpdate      *bool   `json:"autoUpdate"`
	Error           *string `json:"error"`
}

func Check(currentVersion, channel string) Status {
	disabled := false
	return Status{
		CurrentVersion: currentVersion,
		Channel:        channel,
		AutoUpdate:     &disabled,
	}
}

func BlockedMessage() string {
	return fmt.Sprintf(
		"Gork Build never installs from vendor (x.ai) update channels - that would replace this privacy fork with official Grok Build.\n\n"+
			"Rebuild from source instead:\n  git pull && go build -o gork ./cmd/gork\n\n"+
			"Community releases (when published): %s",
		ReleasesURL,
	)
}
