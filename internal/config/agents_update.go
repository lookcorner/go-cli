package config

import (
	"fmt"
)

type AgentSettings struct {
	Toggle  map[string]bool
	Default string
}

func LoadAgentSettings(path string) (AgentSettings, error) {
	if path == "" {
		var err error
		path, err = discoverDefaultPath()
		if err != nil {
			return AgentSettings{}, err
		}
	}
	root, err := readConfigMap(path)
	if err != nil {
		return AgentSettings{}, err
	}
	settings := AgentSettings{Toggle: map[string]bool{}}
	if section, ok := root["subagents"].(map[string]any); ok {
		if toggle, ok := section["toggle"].(map[string]any); ok {
			for name, value := range toggle {
				if enabled, ok := value.(bool); ok {
					settings.Toggle[name] = enabled
				}
			}
		}
	}
	if section, ok := root["agent"].(map[string]any); ok {
		if name, ok := section["name"].(string); ok {
			settings.Default = name
		}
	}
	return settings, nil
}

func UpdateAgentToggle(path, name string, enabled bool) error {
	if name == "" {
		return fmt.Errorf("agent name is required")
	}
	return updateUserConfig(path, func(root map[string]any) error {
		section, ok := root["subagents"].(map[string]any)
		if !ok {
			section = map[string]any{}
			root["subagents"] = section
		}
		toggle, ok := section["toggle"].(map[string]any)
		if !ok {
			toggle = map[string]any{}
			section["toggle"] = toggle
		}
		toggle[name] = enabled
		return nil
	})
}

func UpdateDefaultAgent(path, name string) error {
	return updateUserConfig(path, func(root map[string]any) error {
		if name == "" {
			if section, ok := root["agent"].(map[string]any); ok {
				delete(section, "name")
				if len(section) == 0 {
					delete(root, "agent")
				}
			}
			return nil
		}
		section, ok := root["agent"].(map[string]any)
		if !ok {
			section = map[string]any{}
			root["agent"] = section
		}
		section["name"] = name
		return nil
	})
}
