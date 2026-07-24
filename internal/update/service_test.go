package update

import (
	"strings"
	"testing"
)

func TestCheckReportsPrivacyBuildPolicy(t *testing.T) {
	status := Check("0.1.0", "alpha")
	if status.CurrentVersion != "0.1.0" || status.Channel != "alpha" ||
		status.LatestVersion != nil || status.UpdateAvailable || status.Installer != nil ||
		status.AutoUpdate == nil || *status.AutoUpdate || status.Error != nil {
		t.Fatalf("status=%#v", status)
	}
}

func TestBlockedMessageUsesCommunitySources(t *testing.T) {
	message := BlockedMessage()
	for _, want := range []string{"never installs from vendor", "go build", ReleasesURL} {
		if !strings.Contains(message, want) {
			t.Fatalf("message %q missing %q", message, want)
		}
	}
}
