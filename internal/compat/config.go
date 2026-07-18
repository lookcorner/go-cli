package compat

type Vendor struct {
	Skills bool
	Rules  bool
	Agents bool
	Mcps   bool
}

type Config struct {
	Cursor Vendor
	Claude Vendor
}

func Default() Config {
	on := Vendor{Skills: true, Rules: true, Agents: true, Mcps: true}
	return Config{Cursor: on, Claude: on}
}
