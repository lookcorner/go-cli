package config

import "encoding/json"

const (
	minDisplayCadenceMS = 1
	maxDisplayCadenceMS = 100
)

func (s *RefreshSettings) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil
	}
	boolValue := func(name string) *bool {
		var value bool
		if raw := fields[name]; len(raw) > 0 && json.Unmarshal(raw, &value) == nil {
			return &value
		}
		return nil
	}
	uintValue := func(name string) *uint32 {
		var value uint32
		if raw := fields[name]; len(raw) > 0 && json.Unmarshal(raw, &value) == nil {
			return &value
		}
		return nil
	}
	s.ProbeEnabled = boolValue("probe_enabled")
	s.AutoCadenceEnabled = boolValue("auto_cadence_enabled")
	s.FloorMS = uintValue("floor_ms")
	s.CeilingMS = uintValue("ceiling_ms")
	s.MinHz = uintValue("min_hz")
	s.MaxHz = uintValue("max_hz")
	return nil
}

func applyDisplayRefreshFile(cfg *Config, values *RefreshSettings) {
	if values == nil {
		return
	}
	if values.ProbeEnabled != nil {
		cfg.UI.DisplayRefresh.ProbeEnabled = *values.ProbeEnabled
		cfg.uiRefreshConfigured.probe = true
	}
	if values.AutoCadenceEnabled != nil {
		cfg.UI.DisplayRefresh.AutoCadenceEnabled = *values.AutoCadenceEnabled
		cfg.uiRefreshConfigured.auto = true
	}
	if values.FloorMS != nil {
		cfg.UI.DisplayRefresh.FloorMS = clampDisplayCadence(*values.FloorMS)
		cfg.uiRefreshConfigured.floor = true
	}
	if values.CeilingMS != nil {
		cfg.UI.DisplayRefresh.CeilingMS = clampDisplayCadence(*values.CeilingMS)
		cfg.uiRefreshConfigured.ceiling = true
	}
	if values.MinHz != nil {
		cfg.UI.DisplayRefresh.MinHz = *values.MinHz
		cfg.uiRefreshConfigured.minHz = true
	}
	if values.MaxHz != nil {
		cfg.UI.DisplayRefresh.MaxHz = *values.MaxHz
		cfg.uiRefreshConfigured.maxHz = true
	}
	normalizeDisplayRefresh(&cfg.UI.DisplayRefresh, values.FloorMS != nil, values.CeilingMS != nil, values.MinHz != nil, values.MaxHz != nil)
}

func applyRemoteDisplayRefresh(cfg *Config, values *RefreshSettings) {
	if values == nil {
		return
	}
	configured := cfg.uiRefreshConfigured
	setFloor := !configured.floor && values.FloorMS != nil
	setCeiling := !configured.ceiling && values.CeilingMS != nil
	setMinHz := !configured.minHz && values.MinHz != nil
	setMaxHz := !configured.maxHz && values.MaxHz != nil
	if !configured.probe && values.ProbeEnabled != nil {
		cfg.UI.DisplayRefresh.ProbeEnabled = *values.ProbeEnabled
	}
	if !configured.auto && values.AutoCadenceEnabled != nil {
		cfg.UI.DisplayRefresh.AutoCadenceEnabled = *values.AutoCadenceEnabled
	}
	if setFloor {
		cfg.UI.DisplayRefresh.FloorMS = clampDisplayCadence(*values.FloorMS)
	}
	if setCeiling {
		cfg.UI.DisplayRefresh.CeilingMS = clampDisplayCadence(*values.CeilingMS)
	}
	if setMinHz {
		cfg.UI.DisplayRefresh.MinHz = *values.MinHz
	}
	if setMaxHz {
		cfg.UI.DisplayRefresh.MaxHz = *values.MaxHz
	}
	if cfg.UI.DisplayRefresh.FloorMS > cfg.UI.DisplayRefresh.CeilingMS && configured.floor {
		cfg.UI.DisplayRefresh.CeilingMS = cfg.UI.DisplayRefresh.FloorMS
		setFloor, setCeiling = false, false
	} else if cfg.UI.DisplayRefresh.FloorMS > cfg.UI.DisplayRefresh.CeilingMS && configured.ceiling {
		cfg.UI.DisplayRefresh.FloorMS = cfg.UI.DisplayRefresh.CeilingMS
		setFloor, setCeiling = false, false
	}
	if cfg.UI.DisplayRefresh.MinHz > cfg.UI.DisplayRefresh.MaxHz && configured.minHz {
		cfg.UI.DisplayRefresh.MaxHz = cfg.UI.DisplayRefresh.MinHz
		setMinHz, setMaxHz = false, false
	} else if cfg.UI.DisplayRefresh.MinHz > cfg.UI.DisplayRefresh.MaxHz && configured.maxHz {
		cfg.UI.DisplayRefresh.MinHz = cfg.UI.DisplayRefresh.MaxHz
		setMinHz, setMaxHz = false, false
	}
	normalizeDisplayRefresh(&cfg.UI.DisplayRefresh, setFloor, setCeiling, setMinHz, setMaxHz)
}

func normalizeDisplayRefresh(value *DisplayRefreshConfig, floorSet, ceilingSet, minHzSet, maxHzSet bool) {
	value.FloorMS = clampDisplayCadence(value.FloorMS)
	value.CeilingMS = clampDisplayCadence(value.CeilingMS)
	if value.FloorMS > value.CeilingMS {
		switch {
		case floorSet && !ceilingSet:
			value.CeilingMS = value.FloorMS
		case ceilingSet && !floorSet:
			value.FloorMS = value.CeilingMS
		default:
			value.FloorMS, value.CeilingMS = 8, 16
		}
	}
	if value.MinHz > value.MaxHz {
		switch {
		case minHzSet && !maxHzSet:
			value.MaxHz = value.MinHz
		case maxHzSet && !minHzSet:
			value.MinHz = value.MaxHz
		default:
			value.MinHz, value.MaxHz = 55, 165
		}
	}
}

func clampDisplayCadence(value uint32) uint32 {
	return min(max(value, minDisplayCadenceMS), maxDisplayCadenceMS)
}

func UpdateDisplayRefreshAutoCadence(path string, enabled bool) error {
	return updateUserConfig(path, func(root map[string]any) error {
		ui, _ := root["ui"].(map[string]any)
		if ui == nil {
			ui = make(map[string]any)
		}
		display, _ := ui["display_refresh"].(map[string]any)
		if display == nil {
			display = make(map[string]any)
		}
		display["auto_cadence_enabled"] = enabled
		ui["display_refresh"] = display
		root["ui"] = ui
		return nil
	})
}
