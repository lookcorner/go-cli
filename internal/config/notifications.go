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
	if disk.Events != nil {
		merged.Events = make([]string, 0, len(disk.Events))
		for _, event := range disk.Events {
			merged.Events = append(merged.Events, strings.ToLower(strings.TrimSpace(event)))
		}
	}
	if !merged.valid() {
		return current
	}
	return merged
}
