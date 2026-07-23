package announcement

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Announcement struct {
	ID          *string `json:"id"`
	Message     *string `json:"message"`
	Severity    *string `json:"severity"`
	Title       *string `json:"title"`
	CTA         *CTA    `json:"cta"`
	UpdatedAt   *string `json:"updated_at"`
	ExpiresAt   *string `json:"expires_at"`
	Dismissible *bool   `json:"dismissible"`
	Persistent  *bool   `json:"persistent"`
}

type CTA struct {
	Label   *string `json:"label"`
	URL     *string `json:"url"`
	Caption *string `json:"caption"`
}

type Service struct {
	items  []Announcement
	hidden map[string]bool
	path   string
	now    func() time.Time
}

func New(items []Announcement, home string) *Service {
	if override := strings.TrimSpace(os.Getenv("GROK_ANNOUNCEMENTS_OVERRIDE")); override != "" {
		var parsed []Announcement
		if json.Unmarshal([]byte(override), &parsed) == nil {
			items = parsed
		}
	}
	service := &Service{
		items: append([]Announcement(nil), items...), hidden: make(map[string]bool),
		path: filepath.Join(home, "announcements.json"), now: time.Now,
	}
	var state struct {
		HiddenIDs []string `json:"hidden_ids"`
	}
	if data, err := os.ReadFile(service.path); err == nil && json.Unmarshal(data, &state) == nil {
		for _, id := range state.HiddenIDs {
			service.hidden[id] = true
		}
		live := make(map[string]bool)
		for _, key := range service.sessionKeys() {
			live[key] = true
		}
		changed := false
		for id := range service.hidden {
			if !live[id] {
				delete(service.hidden, id)
				changed = true
			}
		}
		if changed {
			_ = service.persist()
		}
	}
	return service
}

func (s *Service) Available() bool {
	return s != nil && len(s.sessionKeys()) > 0
}

func (s *Service) Current() (Announcement, bool) {
	if s == nil {
		return Announcement{}, false
	}
	now := s.now()
	for _, severity := range []string{"critical", "promo"} {
		for _, item := range s.items {
			if visible(item, severity, now) && (!dismissible(item) || !s.hidden[hideKey(item)]) {
				return item, true
			}
		}
	}
	return Announcement{}, false
}

func (s *Service) Hide() error {
	item, ok := s.Current()
	if !ok || !dismissible(item) {
		return nil
	}
	s.hidden[hideKey(item)] = true
	return s.persist()
}

func (s *Service) Show() error {
	if s == nil {
		return nil
	}
	changed := false
	for _, key := range s.sessionKeys() {
		if s.hidden[key] {
			delete(s.hidden, key)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.persist()
}

func (s *Service) sessionKeys() []string {
	if s == nil {
		return nil
	}
	now := s.now()
	keys := make([]string, 0, len(s.items))
	for _, item := range s.items {
		if visible(item, strings.TrimSpace(value(item.Severity)), now) && (value(item.Severity) == "critical" || value(item.Severity) == "promo") {
			keys = append(keys, hideKey(item))
		}
	}
	return keys
}

func (s *Service) persist() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	ids := make([]string, 0, len(s.hidden))
	for id := range s.hidden {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	data, err := json.Marshal(struct {
		HiddenIDs []string `json:"hidden_ids"`
	}{ids})
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

func visible(item Announcement, severity string, now time.Time) bool {
	if strings.TrimSpace(value(item.Message)) == "" || value(item.Severity) != severity {
		return false
	}
	if expires := strings.TrimSpace(value(item.ExpiresAt)); expires != "" {
		if at, err := time.Parse(time.RFC3339, expires); err == nil && !at.After(now) {
			return false
		}
	}
	return true
}

func dismissible(item Announcement) bool { return item.Dismissible == nil || *item.Dismissible }

func hideKey(item Announcement) string {
	if id := strings.TrimSpace(value(item.ID)); id != "" {
		return id
	}
	return "content:" + value(item.Title) + "\x1f" + value(item.Message)
}

func value(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
