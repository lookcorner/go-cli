package tui

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"

	_ "golang.org/x/image/webp"
)

// sixelMaxDimension bounds inline previews; larger images stay metadata-only.
const sixelMaxDimension = 1024

// sixelImageSequence encodes an image as a sixel DCS stream using a fixed
// 216-color RGB cube with direct level quantization, alpha skipping, and
// run-length compression.
func sixelImageSequence(data []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width == 0 || height == 0 {
		return nil, errors.New("empty image")
	}
	if width > sixelMaxDimension || height > sixelMaxDimension {
		return nil, fmt.Errorf("image %dx%d exceeds the %dpx inline bound", width, height, sixelMaxDimension)
	}

	indexed := make([][]int, height)
	used := make([]bool, 216)
	for y := 0; y < height; y++ {
		row := make([]int, width)
		for x := 0; x < width; x++ {
			r, g, b, a := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			if a < 0x8000 {
				row[x] = -1
				continue
			}
			index := sixelLevel(uint8(r>>8))*36 + sixelLevel(uint8(g>>8))*6 + sixelLevel(uint8(b>>8))
			row[x] = index
			used[index] = true
		}
		indexed[y] = row
	}

	var out strings.Builder
	out.WriteString("\x1bPq")
	fmt.Fprintf(&out, `"1;1;%d;%d`, width, height)
	for index := 0; index < 216; index++ {
		if used[index] {
			fmt.Fprintf(&out, "#%d;2;%d;%d;%d", index, index/36*20, index/6%6*20, index%6*20)
		}
	}
	for band := 0; band*6 < height; band++ {
		if band > 0 {
			out.WriteByte('-')
		}
		first := true
		for color := 0; color < 216; color++ {
			if !used[color] || !sixelBandHasColor(indexed[band*6:min(band*6+6, height)], color) {
				continue
			}
			if !first {
				out.WriteByte('$')
			}
			first = false
			fmt.Fprintf(&out, "#%d", color)
			run := 0
			var char byte
			flush := func() {
				if run == 0 {
					return
				}
				if run >= 3 {
					fmt.Fprintf(&out, "!%d%c", run, char)
				} else {
					out.WriteString(strings.Repeat(string(char), run))
				}
				run = 0
			}
			for x := 0; x < width; x++ {
				var bits byte
				for dy := 0; dy < 6 && band*6+dy < height; dy++ {
					if indexed[band*6+dy][x] == color {
						bits |= 1 << dy
					}
				}
				next := 63 + bits
				if next == char && run > 0 {
					run++
					continue
				}
				flush()
				char, run = next, 1
			}
			flush()
		}
	}
	out.WriteString("\x1b\\")
	return []byte(out.String()), nil
}

// sixelLevel maps one 8-bit channel to a 0-5 cube level.
func sixelLevel(channel uint8) int {
	return (int(channel)*5 + 127) / 255
}

func sixelBandHasColor(rows [][]int, color int) bool {
	for _, row := range rows {
		for _, index := range row {
			if index == color {
				return true
			}
		}
	}
	return false
}
