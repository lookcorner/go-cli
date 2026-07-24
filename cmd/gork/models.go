package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/config"
)

func runModels(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("models", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to config file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected models argument %q", cleanCLIText(flags.Arg(0)))
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if err := prepareManagedPolicy(&cfg, *configPath, stderr); err != nil {
		return err
	}
	if err := cfg.ValidateAuthPolicy(); err != nil {
		return err
	}
	status, err := loadModelsCatalog(context.Background(), &cfg, stderr)
	if err != nil {
		return err
	}
	printModels(stdout, cfg, status)
	return nil
}

func loadModelsCatalog(ctx context.Context, cfg *config.Config, stderr io.Writer) (string, error) {
	authPath, _ := auth.DefaultPath()
	authConfig := auth.DefaultConfig()
	applyAuthPolicy(&authConfig, *cfg)
	status := modelsAuthStatus(*cfg, authPath, authConfig)

	method, token := "api_key", modelsCatalogAPIKey(*cfg, authPath)
	var provider func(context.Context, string) (string, error)
	switch {
	case cfg.DeploymentKey != "":
		method, token = "deployment", cfg.DeploymentKey
	case cfg.PreferredAuthMethod != "api_key":
		if _, err := auth.Load(authPath, authConfig.Scope()); err == nil {
			method = "session"
			provider = newAuthTokenProvider(*cfg, authPath, authConfig, stderr)
			token, err = provider(ctx, "")
			if err != nil {
				return "", fmt.Errorf("load dynamic credentials: %w", err)
			}
			cfg.APIKey = token
		}
	}
	if token == "" {
		return status, nil
	}
	cfg.APIKey = token

	authMethod, origin := modelCacheIdentity(*cfg, provider)
	cache, ok := config.LoadModelCache(authMethod, origin)
	if !ok {
		credential := auth.Credential{}
		if method == "session" {
			credential, _ = auth.Load(authPath, authConfig.Scope())
		}
		fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		var err error
		cache, err = config.FetchModelCache(fetchCtx, config.ModelFetchRequest{
			AuthMethod: authMethod, Origin: origin, InferenceBaseURL: cfg.BaseURL, Token: token,
			TokenHeader: auth.DefaultTokenHeader, UserID: credential.UserID, Email: credential.Email,
			HTTP: &http.Client{Timeout: 30 * time.Second},
		})
		if err != nil {
			if len(cfg.ModelProfiles) == 0 {
				return "", err
			}
			fmt.Fprintf(stderr, "Model catalog refresh failed; using configured models: %v\n", err)
			return status, nil
		}
	}
	cfg.ApplyModelCache(cache)
	return status, nil
}

func modelsCatalogAPIKey(cfg config.Config, authPath string) string {
	for _, name := range []string{"GORK_API_KEY", "XAI_API_KEY", "OPENAI_API_KEY", "GROK_CODE_XAI_API_KEY"} {
		if key := strings.TrimSpace(os.Getenv(name)); key != "" {
			return key
		}
	}
	if profile, ok := cfg.ModelProfiles[cfg.DefaultModelID]; !ok || profile.APIKey == "" || cfg.APIKey != profile.APIKey {
		return resolveACPAPIKey(cfg, authPath)
	}
	if credential, err := auth.Load(authPath, auth.APIKeyScope); err == nil {
		return credential.Key
	}
	return ""
}

func modelsAuthStatus(cfg config.Config, authPath string, authConfig auth.Config) string {
	if key, ok := auth.ReadAPIKeyEnvironment(); ok && strings.TrimSpace(key) != "" {
		return "You are using XAI_API_KEY."
	}
	if _, err := auth.Load(authPath, authConfig.Scope()); err == nil {
		return "You are logged in with " + authHost(authConfig.Issuer) + "."
	}
	if !cfg.DisableAPIKeyAuth && !cfg.ForceLoginTeamConfigured {
		names := make([]string, 0, len(cfg.ModelProfiles))
		for id := range cfg.ModelProfiles {
			names = append(names, id)
		}
		sort.Strings(names)
		for _, id := range names {
			if strings.TrimSpace(cfg.ModelProfiles[id].APIKey) != "" {
				return fmt.Sprintf("Model %q is using its own API key.", id)
			}
		}
	}
	if cfg.DeploymentKey != "" {
		return "You are authenticated via deployment key."
	}
	return "You are not authenticated."
}

func authHost(issuer string) string {
	parsed, err := url.Parse(issuer)
	if err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return strings.TrimSpace(issuer)
}

func printModels(output io.Writer, cfg config.Config, status string) {
	defaultID := acpSessionModelID(cfg, "")
	fmt.Fprintln(output, status)
	fmt.Fprintln(output)
	fmt.Fprintf(output, "Default model: %s\n\nAvailable models:\n", cleanCLIText(defaultID))

	type listedModel struct {
		id      string
		current bool
	}
	models := make([]listedModel, 0, len(cfg.ModelSlugs())+1)
	seen := make(map[string]bool)
	for _, slug := range cfg.ModelSlugs() {
		id, resolved, ok := cfg.ResolveModelEntry(slug)
		if !ok || seen[id] || !cfg.ModelVisible(id, resolved.Model) {
			continue
		}
		seen[id] = true
		models = append(models, listedModel{id: id, current: id == defaultID})
	}
	if defaultID != "" && !seen[defaultID] {
		if id, resolved, ok := cfg.ResolveModelEntry(defaultID); ok && cfg.ModelSelectable(id, resolved.Model) {
			models = append(models, listedModel{id: id, current: true})
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].id < models[j].id })
	for _, model := range models {
		marker, suffix := "-", ""
		if model.current {
			marker, suffix = "*", " (default)"
		}
		fmt.Fprintf(output, "  %s %s%s\n", marker, cleanCLIText(model.id), suffix)
	}
	if len(models) == 0 {
		fmt.Fprintln(output, "  (none)")
	}
}
