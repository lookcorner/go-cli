package tui

import (
	"context"
	"crypto/sha256"
	"image"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	appclipboard "github.com/lookcorner/go-cli/internal/clipboard"
)

func TestImageInputHintFocusProbeAndPaste(t *testing.T) {
	data := encodeTestPNG(t, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	reads := 0
	m := &model{
		ctx:   context.Background(),
		width: 80, height: 30,
		imageInputHint: imageInputHintState{enabled: true},
		clipboardRead: func(context.Context) (appclipboard.Content, error) {
			reads++
			return appclipboard.Content{MediaType: "image/png", Data: data}, nil
		},
	}
	if !m.View().ReportFocus {
		t.Fatal("enabled image hint did not request focus events")
	}
	updated, command := m.Update(tea.FocusMsg{})
	m = updated.(*model)
	if command == nil || reads != 0 {
		t.Fatalf("command=%v reads=%d", command != nil, reads)
	}
	updated, tick := m.Update(command())
	m = updated.(*model)
	if tick == nil || reads != 1 || !m.imageInputHint.active ||
		!strings.Contains(m.View().Content, "Image in clipboard · Ctrl-V to paste") {
		t.Fatalf("tick=%v reads=%d hint=%#v view=%q", tick != nil, reads, m.imageInputHint, m.View().Content)
	}

	updated, paste := m.Update(tea.KeyPressMsg(tea.Key{Code: 'v', Text: "v", Mod: tea.ModCtrl}))
	m = updated.(*model)
	if paste == nil || m.imageInputHint.active {
		t.Fatalf("paste=%v active=%v", paste != nil, m.imageInputHint.active)
	}
	updated, _ = m.Update(paste())
	m = updated.(*model)
	if len(m.promptImages) != 1 || m.imageInputHint.active {
		t.Fatalf("images=%d active=%v", len(m.promptImages), m.imageInputHint.active)
	}
}

func TestImageInputHintDisabledDoesNotRequestFocusEvents(t *testing.T) {
	m := &model{width: 80, height: 30}
	if m.View().ReportFocus {
		t.Fatal("disabled image hint requested focus events")
	}
	updated, command := m.Update(tea.FocusMsg{})
	_ = updated.(*model)
	if command != nil {
		t.Fatal("disabled image hint probed the clipboard")
	}
}

func TestImageInputHintSuppressesTextDuplicatesCooldownAndOcclusion(t *testing.T) {
	imageOne := encodeTestPNG(t, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	imageTwo := encodeTestPNG(t, image.NewRGBA(image.Rect(0, 0, 3, 3)))
	m := &model{ctx: context.Background(), width: 80, height: 30, imageInputHint: imageInputHintState{enabled: true}}

	for _, content := range []appclipboard.Content{
		{Text: "plain text"},
		{MediaType: "image/png", Data: imageOne},
		{MediaType: "image/png", Data: imageOne},
		{MediaType: "image/png", Data: imageTwo},
	} {
		updated, command := m.Update(imageInputHintProbeEvent{content: content})
		m = updated.(*model)
		if content.Text != "" && (command != nil || m.imageInputHint.active) {
			t.Fatalf("text probe command=%v active=%v", command != nil, m.imageInputHint.active)
		}
		if content.Text == "" && len(content.Data) > 0 && m.imageInputHint.lastFired.IsZero() {
			t.Fatal("first image did not fire")
		}
	}
	if m.imageInputHint.lastDigest != digestForTest(imageOne) {
		t.Fatal("duplicate or cooldown image replaced the committed digest")
	}
	m.imageInputHint.active = false
	m.imageInputHint.lastFired = time.Now().Add(-31 * time.Second)
	updated, command := m.Update(imageInputHintProbeEvent{content: appclipboard.Content{MediaType: "image/png", Data: imageTwo}})
	m = updated.(*model)
	if command == nil || !m.imageInputHint.active || m.imageInputHint.lastDigest != digestForTest(imageTwo) {
		t.Fatalf("command=%v hint=%#v", command != nil, m.imageInputHint)
	}

	reads := 0
	m.settings = &settingsState{}
	m.clipboardRead = func(context.Context) (appclipboard.Content, error) {
		reads++
		return appclipboard.Content{}, nil
	}
	updated, command = m.Update(tea.FocusMsg{})
	_ = updated.(*model)
	if command != nil || reads != 0 {
		t.Fatalf("occluded command=%v reads=%d", command != nil, reads)
	}
}

func TestImageInputHintSettingRollsBack(t *testing.T) {
	m := &model{
		settings:       &settingsState{selected: 27},
		imageInputHint: imageInputHintState{enabled: true, active: true, persist: func(bool) error { return context.Canceled }},
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.imageInputHint.enabled || !m.imageInputHint.active ||
		m.settings.err != context.Canceled.Error() || m.status != "setting update failed" {
		t.Fatalf("command=%v hint=%#v err=%q status=%q", command != nil, m.imageInputHint, m.settings.err, m.status)
	}
}

func digestForTest(data []byte) [32]byte {
	return sha256.Sum256(data)
}
