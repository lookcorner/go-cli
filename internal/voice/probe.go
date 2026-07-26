package voice

// InputProbe is a passive microphone/recorder lookup for doctor diagnostics.
// When Supported is false the Voice section is omitted entirely.
type InputProbe struct {
	Supported bool
	Name      string
	Detail    string
	Error     string
}

// ProbeInput reports the default capture device or Linux recorder without
// opening an audio stream. lookPath is used on Linux; other platforms ignore it.
func ProbeInput(lookPath func(string) (string, error)) InputProbe {
	if !Supported() {
		return InputProbe{}
	}
	if lookPath == nil {
		lookPath = func(string) (string, error) { return "", errNotFound{} }
	}
	probe := platformProbeInput(lookPath)
	probe.Supported = true
	return probe
}

type errNotFound struct{}

func (errNotFound) Error() string { return "not found" }
