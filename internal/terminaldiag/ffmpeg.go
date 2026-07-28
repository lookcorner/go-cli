package terminaldiag

// ffmpegAvailable reports whether ffmpeg is on PATH (needed by /play-video).
func ffmpegAvailable(lookPath func(string) (string, error)) bool {
	if lookPath == nil {
		return false
	}
	_, err := lookPath("ffmpeg")
	return err == nil
}

func ffmpegFinding(available bool) string {
	if available {
		return ""
	}
	return "`/play-video` needs ffmpeg on PATH to extract frames.\n" +
		"    Install ffmpeg (for example `brew install ffmpeg` or your distro package), then re-run `/doctor` or `gork doctor`."
}
