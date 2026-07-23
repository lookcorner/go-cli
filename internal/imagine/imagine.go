package imagine

import "strings"

const (
	ImageTool = "image_gen"
	VideoTool = "image_to_video"
)

type Command struct {
	Display      string
	Instruction  string
	Usage        string
	RequiredTool string
}

func Parse(input string) (Command, bool) {
	trimmed := strings.TrimSpace(input)
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return Command{}, false
	}
	name := fields[0]
	prompt := strings.TrimSpace(strings.TrimPrefix(trimmed, name))
	switch name {
	case "/imagine":
		command := Command{Display: name, RequiredTool: ImageTool, Usage: "Usage: /imagine <description>\nProvide a text description to generate an image."}
		if prompt != "" {
			command.Display += " " + prompt
			command.Instruction = "Call the image_gen tool immediately, passing the user's prompt below verbatim - do not rewrite, embellish, or expand it. After the tool completes, briefly acknowledge and mention where the image was saved.\n\nPrompt: " + prompt
		}
		return command, true
	case "/imagine-video":
		command := Command{Display: name, RequiredTool: VideoTool, Usage: "Usage: /imagine-video <description>\nProvide a text description to generate a video."}
		if prompt != "" {
			command.Display += " " + prompt
			command.Instruction = videoInstruction + "\n\nUser prompt: " + prompt
		}
		return command, true
	default:
		return Command{}, false
	}
}

const videoInstruction = `# Imagine Video

Video starts from an image - there is no text-to-video tool. Default to image_to_video; use reference_to_video only when the user explicitly asks for it or a shot genuinely needs multiple reference images.

## Default: single clip

Unless the user asks for a long video, multiple scenes, or a multi-shot sequence, generate one video:

1. Create a source image with image_gen that stages the first frame (composition, subject, lighting).
2. Call image_to_video with that image and a short prompt describing the motion or camera move (1-2 sentences, present tense).
3. After the tool completes, mention the saved file path so the user can find it.

## Longer / multi-shot videos

When the user requests a longer video, multiple scenes, or a narrative sequence:

1. Plan the story as shots - break the idea into distinct shots, one beat each.
2. Favor frequent, short shots - prefer more 6s clips over fewer long ones; more cuts keep it dynamic.
3. Create each shot's source image with image_gen (or image_edit to combine references), keeping characters and settings consistent across shots.
4. Animate each shot with image_to_video - the source image becomes frame 1.
5. Assemble with FFmpeg using stream copy (ffmpeg -f concat ... -c copy - never re-encode). Keep every shot at the same resolution and frame rate so the concat works. After assembly, mention the final output path.

## Shot guidance

- Prompt-craft: one short, vivid moment in present tense with a clear camera movement, in 1-2 sentences.
- Minimal but interesting: one clear subject, one simple motion or camera move per shot. Avoid complex multi-action animation; make the shot compelling through composition, lighting, and a strong moment.
- Complex source image? Intricate frames (busy geometry, fine detail, heavy reflections) warp when animated. Keep the subject fixed and move only the camera (slow push-in, orbit, or parallax), or break into simpler shots. For new shots, generate a simpler, animation-friendly base image rather than animating a busy one.
- image_to_video animates from frame 1 - stage the first frame with image_gen/image_edit before animating.
- Aspect ratio: set it on the source image (image_gen aspect_ratio); do not re-crop an existing video.
- Duration: 6s or 10s only (prefer 6s); round to the nearest.
- Real people: reference-first - drive the video from a verified reference image; never animate a named person without one.
- Do not loop the same clip unless asked.`
