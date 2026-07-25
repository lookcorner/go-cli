package config

func UpdateQuestionTimeout(path string, enabled bool) error {
	return updateUserConfig(path, func(root map[string]any) error {
		toolset, _ := root["toolset"].(map[string]any)
		if toolset == nil {
			toolset = make(map[string]any)
		}
		questions, _ := toolset["ask_user_question"].(map[string]any)
		if questions == nil {
			questions = make(map[string]any)
		}
		questions["timeout_enabled"] = enabled
		toolset["ask_user_question"] = questions
		root["toolset"] = toolset
		return nil
	})
}
