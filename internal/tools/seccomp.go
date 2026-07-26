package tools

// SeccompNamespaceMarker is argv[1] when gork re-execs itself inside bwrap to
// install the namespace lockdown filter before the real command.
const SeccompNamespaceMarker = "__GROK_SECCOMP_NS__"
