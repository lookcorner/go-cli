package tools

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

const maxPPTXXMLBytes = 64 << 20

func extractPPTXText(data []byte) (string, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open archive: %w", err)
	}
	files := make(map[string]*zip.File, len(archive.File))
	var slides []int
	for _, file := range archive.File {
		files[file.Name] = file
		name := strings.TrimSuffix(strings.TrimPrefix(file.Name, "ppt/slides/slide"), ".xml")
		if file.Name == "ppt/slides/slide"+name+".xml" {
			if number, err := strconv.Atoi(name); err == nil && number > 0 {
				slides = append(slides, number)
			}
		}
	}
	if len(slides) == 0 {
		return "", errors.New("no slides found")
	}
	sort.Ints(slides)
	var output strings.Builder
	for _, number := range slides {
		text, err := drawingMLText(files[fmt.Sprintf("ppt/slides/slide%d.xml", number)])
		if err != nil {
			return "", fmt.Errorf("parse slide %d: %w", number, err)
		}
		if output.Len() > 0 {
			output.WriteString("\n\n")
		}
		fmt.Fprintf(&output, "--- Slide %d ---\n%s", number, text)
		if notes, err := drawingMLText(files[fmt.Sprintf("ppt/notesSlides/notesSlide%d.xml", number)]); err == nil && notes != "" {
			output.WriteString("\n\nSpeaker Notes:\n")
			output.WriteString(notes)
		}
	}
	return output.String(), nil
}

func drawingMLText(file *zip.File) (string, error) {
	if file == nil {
		return "", errors.New("entry not found")
	}
	if file.UncompressedSize64 > maxPPTXXMLBytes {
		return "", errors.New("XML entry exceeds size limit")
	}
	reader, err := file.Open()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	decoder := xml.NewDecoder(io.LimitReader(reader, maxPPTXXMLBytes+1))
	var output strings.Builder
	inText, lastNewline := false, false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		switch value := token.(type) {
		case xml.StartElement:
			if value.Name.Local == "t" {
				inText = true
			}
		case xml.CharData:
			if inText {
				output.Write(value)
				if len(value) > 0 {
					lastNewline = value[len(value)-1] == '\n'
				}
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "t":
				inText = false
			case "p":
				if output.Len() > 0 && !lastNewline {
					output.WriteByte('\n')
					lastNewline = true
				}
			}
		}
	}
	return strings.TrimSpace(output.String()), nil
}
