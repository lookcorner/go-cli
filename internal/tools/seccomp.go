package tools

// Seccomp helper argv markers used when gork re-execs itself inside bwrap.
const (
	// SeccompNamespaceMarker installs namespace lockdown only.
	SeccompNamespaceMarker = "__GROK_SECCOMP_NS__"
	// SeccompNamespaceNetMarker installs namespace lockdown plus child network deny.
	SeccompNamespaceNetMarker = "__GROK_SECCOMP_NS_NET__"
)
