package config

import "errors"

func UpdateMarketplace(path string, update func(*MarketplaceConfig)) error {
	if update == nil {
		return errors.New("marketplace update is required")
	}
	return updateUserConfig(path, func(root map[string]any) error {
		settings, err := readConfigSection[MarketplaceConfig](root, "marketplace")
		if err != nil {
			return err
		}
		update(&settings)
		if len(settings.Sources) == 0 {
			delete(root, "marketplace")
		} else {
			root["marketplace"] = map[string]any{"sources": settings.Sources}
		}
		return nil
	})
}
