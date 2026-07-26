//go:build linux

package voice

func platformProbeInput(lookPath func(string) (string, error)) InputProbe {
	for _, name := range []string{"pw-record", "parec", "arecord"} {
		if _, err := lookPath(name); err == nil {
			return InputProbe{
				Name:   name,
				Detail: "system recorder; uses the audio server's default input",
			}
		}
	}
	return InputProbe{Error: "no microphone recorder found; install pw-record, parec, or arecord"}
}
