package compat

type Vendor struct {
	Skills bool
	Rules  bool
	Agents bool
}

type Config struct {
	Cursor Vendor
	Claude Vendor
}

func Default() Config {
	on := Vendor{Skills: true, Rules: true, Agents: true}
	return Config{Cursor: on, Claude: on}
}
