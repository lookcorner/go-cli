package mcp

type ServerConfig struct {
	Type                  string
	Name                  string
	ServerID              string
	Command               string
	Args                  []string
	Env                   map[string]string
	URL                   string
	Headers               map[string]string
	StartupTimeoutSeconds *uint64
	StartupTimeoutMS      *uint64
	ToolTimeoutSeconds    *uint64
	ToolTimeouts          map[string]uint64
	ToolTimeoutMS         *uint64
	ToolTimeoutsMS        map[string]uint64
	Disabled              bool
	DisabledTools         []string
}
