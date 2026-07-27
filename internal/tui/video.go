package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	videoOverlayFPS     = 10.0
	videoOverlayMaxWidth = 640
)

// VideoViewer holds pre-extracted frames for modal terminal playback.
type VideoViewer struct {
	Frames       [][]byte
	Current      int
	Playing      bool
	FPS          float64
	Width        int
	Height       int
	DurationSecs float64
	Title        string
	lastFrame    time.Time
	retransmit   bool
	placementCache OverlayPlacement
	rectCache      OverlayRect
}

// NewVideoViewerStub builds a deterministic viewer for tests (no ffmpeg).
func NewVideoViewerStub(frames [][]byte, width, height int, title string) *VideoViewer {
	if len(frames) == 0 {
		frames = [][]byte{{}}
	}
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	return &VideoViewer{
		Frames: frames, Playing: true, FPS: videoOverlayFPS,
		Width: width, Height: height,
		DurationSecs: float64(len(frames)) / videoOverlayFPS,
		Title: title, lastFrame: time.Now(), retransmit: true,
	}
}

// FFmpegAvailable reports whether ffmpeg is on PATH.
func FFmpegAvailable() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// OpenVideoFromPath extracts frames with ffmpeg. Returns nil when ffmpeg is
// missing, the terminal has no overlay graphics, or decode fails.
func OpenVideoFromPath(path string, protocol imageProtocol) (*VideoViewer, error) {
	if !OverlaySupportsPixels(protocol) {
		return nil, fmt.Errorf("video overlay needs Kitty or iTerm2 graphics")
	}
	if !FFmpegAvailable() {
		return nil, fmt.Errorf("ffmpeg not found on PATH")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("video path is a directory")
	}
	width, height, duration, sourceFPS, err := ffprobeMetadata(path)
	if err != nil {
		// Fall back to defaults when ffprobe is unavailable.
		width, height, duration, sourceFPS = 640, 360, 0, videoOverlayFPS
	}
	targetFPS := videoOverlayFPS
	if sourceFPS > 0 && sourceFPS < targetFPS {
		targetFPS = sourceFPS
	}
	ext := "png"
	if protocol == imageProtocolITerm2 {
		ext = "jpg"
	}
	vf := fmt.Sprintf("fps=%g", targetFPS)
	if width > videoOverlayMaxWidth {
		vf = fmt.Sprintf("fps=%g,scale=%d:-2", targetFPS, videoOverlayMaxWidth)
		width = videoOverlayMaxWidth
	}
	tmp, err := os.MkdirTemp("", "gork-video-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	pattern := filepath.Join(tmp, "%06d."+ext)
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-i", path, "-vf", vf, "-q:v", "5", pattern)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg extract: %w", err)
	}
	frames, err := loadVideoFrames(tmp, ext)
	if err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("ffmpeg produced no frames")
	}
	if duration <= 0 {
		duration = float64(len(frames)) / targetFPS
	}
	title := filepath.Base(path)
	return &VideoViewer{
		Frames: frames, Playing: true, FPS: targetFPS,
		Width: width, Height: height, DurationSecs: duration,
		Title: title, lastFrame: time.Now(), retransmit: true,
	}, nil
}

func loadVideoFrames(dir, ext string) ([][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var frames [][]byte
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), "."+ext) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		frames = append(frames, data)
	}
	return frames, nil
}

func ffprobeMetadata(path string) (width, height int, duration, fps float64, err error) {
	if _, lookErr := exec.LookPath("ffprobe"); lookErr != nil {
		return 0, 0, 0, 0, lookErr
	}
	cmd := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height,r_frame_rate:format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path)
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, 0, 0, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 3 {
		return 0, 0, 0, 0, fmt.Errorf("unexpected ffprobe output")
	}
	width, _ = strconv.Atoi(strings.TrimSpace(lines[0]))
	height, _ = strconv.Atoi(strings.TrimSpace(lines[1]))
	fps = parseFrameRate(strings.TrimSpace(lines[2]))
	if len(lines) > 3 {
		duration, _ = strconv.ParseFloat(strings.TrimSpace(lines[3]), 64)
	}
	return width, height, duration, fps, nil
}

func parseFrameRate(value string) float64 {
	parts := strings.Split(value, "/")
	if len(parts) == 2 {
		num, _ := strconv.ParseFloat(parts[0], 64)
		den, _ := strconv.ParseFloat(parts[1], 64)
		if den != 0 {
			return num / den
		}
	}
	fps, _ := strconv.ParseFloat(value, 64)
	return fps
}

func (v *VideoViewer) CurrentFrame() []byte {
	if v == nil || len(v.Frames) == 0 {
		return nil
	}
	return v.Frames[v.Current]
}

func (v *VideoViewer) Progress() float64 {
	if v == nil || len(v.Frames) <= 1 {
		return 0
	}
	return float64(v.Current) / float64(len(v.Frames)-1)
}

func (v *VideoViewer) PositionSecs() float64 {
	if v == nil || v.FPS <= 0 {
		return 0
	}
	return float64(v.Current) / v.FPS
}

// Tick advances playback; returns true when the frame changed.
func (v *VideoViewer) Tick() bool {
	if v == nil || !v.Playing || len(v.Frames) == 0 || v.FPS <= 0 {
		return false
	}
	if time.Since(v.lastFrame) < time.Duration(float64(time.Second)/v.FPS) {
		return false
	}
	v.Current = (v.Current + 1) % len(v.Frames)
	v.lastFrame = time.Now()
	v.retransmit = true
	return true
}

func (v *VideoViewer) TogglePlayPause() {
	if v == nil {
		return
	}
	v.Playing = !v.Playing
	if v.Playing {
		v.lastFrame = time.Now()
	}
}

func (v *VideoViewer) SeekForward() {
	if v == nil || len(v.Frames) == 0 {
		return
	}
	skip := int(v.FPS + 0.5)
	if skip < 1 {
		skip = 1
	}
	v.Current = min(v.Current+skip, len(v.Frames)-1)
	v.lastFrame = time.Now()
	v.retransmit = true
}

func (v *VideoViewer) SeekBackward() {
	if v == nil {
		return
	}
	skip := int(v.FPS + 0.5)
	if skip < 1 {
		skip = 1
	}
	v.Current = max(v.Current-skip, 0)
	v.lastFrame = time.Now()
	v.retransmit = true
}
