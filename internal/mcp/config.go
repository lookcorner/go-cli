package mcp

type ServerConfig struct {
	Type          string
	Name          string
	ServerID      string
	Command       string
	Args          []string
	Env           map[string]string
	URL           string
	Headers       map[string]string
	Disabled      bool
	DisabledTools []string
}
