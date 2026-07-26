package config

import "strings"

// mergeNotifications layers a [ui.notifications] table over the current policy.
// The reference decodes the whole table at once, so an unknown enum value
// discards the layer and leaves the inherited policy in place instead of
// failing startup.
func mergeNotifications(current NotificationsConfig, disk *fileNotificationsConfig) NotificationsConfig {
	if disk == nil {
		return current
	}
	merged := current
	if disk.Method != nil {
		merged.Method = strings.ToLower(strings.TrimSpace(*disk.Method))
	}
	if disk.Condition != nil {
		merged.Condition = strings.ToLower(strings.TrimSpace(*disk.Condition))
	}
	if disk.IdleThresholdSecs != nil {
		merged.IdleThresholdSecs = *disk.IdleThresholdSecs
	}
	if disk.ProgressBar != nil {
		merged.ProgressBar = *disk.ProgressBar
	}
	if disk.SleepPrevention != nil {
		merged.SleepPrevention = *disk.SleepPrevention
	}
	if disk.Events != nil {
		merged.Events = normalizeEventNames(disk.Events)
	}
	if disk.Hooks != nil {
		merged.Hooks = make([]NotificationHookConfig, 0, len(disk.Hooks))
		for _, hook := range disk.Hooks {
			converted := NotificationHookConfig{
				Command:       hook.Command,
				Events:        normalizeEventNames(hook.Events),
				OnlyUnfocused: true,
				TimeoutSecs:   10,
			}
			if hook.OnlyUnfocused != nil {
				converted.OnlyUnfocused = *hook.OnlyUnfocused
			}
			if hook.TimeoutSecs != nil {
				converted.TimeoutSecs = *hook.TimeoutSecs
			}
			merged.Hooks = append(merged.Hooks, converted)
		}
	}
	if !merged.valid() {
		return current
	}
	return merged
}

// normalizeEventNames folds case and padding so event lists compare exactly.
func normalizeEventNames(events []string) []string {
	if events == nil {
		return nil
	}
	normalized := make([]string, 0, len(events))
	for _, event := range events {
		normalized = append(normalized, strings.ToLower(strings.TrimSpace(event)))
	}
	return normalized
}
