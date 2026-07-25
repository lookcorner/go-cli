package compat

type Vendor struct {
	Skills   bool
	Rules    bool
	Agents   bool
	Mcps     bool
	Hooks    bool
	Sessions bool
}

type Config struct {
	Cursor Vendor
	Claude Vendor
	Codex  Vendor
}

func Default() Config {
	on := Vendor{Skills: true, Rules: true, Agents: true, Mcps: true, Hooks: true, Sessions: true}
	return Config{Cursor: on, Claude: on, Codex: on}
}
