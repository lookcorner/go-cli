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
		if len(settings.Sources) == 0 && !settings.OfficialMarketplaceAutoInstalled {
			delete(root, "marketplace")
		} else {
			marketplace := make(map[string]any)
			if len(settings.Sources) > 0 {
				marketplace["sources"] = settings.Sources
			}
			if settings.OfficialMarketplaceAutoInstalled {
				marketplace["official_marketplace_auto_installed"] = true
			}
			root["marketplace"] = marketplace
		}
		return nil
	})
}
