package session

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ExportMarkdown renders completed conversation history without session chrome.
func ExportMarkdown(path string) (string, error) {
	entries, err := DisplayTimeline(path)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	lastAssistant := false
	inTools := false
	for _, entry := range entries {
		switch entry.Kind {
		case "user":
			if entry.Synthetic {
				continue
			}
			if inTools {
				out.WriteByte('\n')
				inTools = false
			}
			out.WriteString("## User\n\n")
			out.WriteString(exportPrompt(entry))
			out.WriteString("\n\n")
			lastAssistant = false
		case "assistant":
			if !lastAssistant {
				if inTools {
					out.WriteByte('\n')
					inTools = false
				}
				out.WriteString("## Assistant\n\n")
			}
			out.WriteString(entry.Text)
			out.WriteString("\n\n")
			lastAssistant = true
		case "tool":
			if entry.Tool == nil {
				continue
			}
			if !inTools {
				out.WriteString("## Tools\n\n")
				inTools = true
			}
			out.WriteString("- ")
			out.WriteString(exportToolSummary(*entry.Tool))
			out.WriteByte('\n')
			lastAssistant = false
		}
	}
	return strings.TrimSpace(out.String()), nil
}

func exportPrompt(entry DisplayEntry) string {
	if len(entry.Content) == 0 {
		return entry.Text
	}
	parts := make([]string, 0, len(entry.Content))
	for _, part := range entry.Content {
		switch part.Type {
		case "text":
			parts = append(parts, part.Text)
		case "image":
			if strings.HasPrefix(part.URI, "http://") || strings.HasPrefix(part.URI, "https://") {
				parts = append(parts, "[Image: "+part.URI+"]")
			} else {
				parts = append(parts, "[Image]")
			}
		}
	}
	return strings.Join(parts, "\n")
}

func exportToolSummary(tool DisplayTool) string {
	arguments := map[string]any{}
	_ = json.Unmarshal(tool.Arguments, &arguments)
	value := func(keys ...string) string {
		for _, key := range keys {
			if text, ok := arguments[key].(string); ok && strings.TrimSpace(text) != "" {
				return text
			}
		}
		return ""
	}
	switch tool.Name {
	case "read_file", "hashline_read":
		return exportToolLabel("Read", value("path", "file_path"))
	case "write_file", "edit_file", "search_replace", "hashline_edit":
		return exportToolLabel("Edit", value("path", "file_path"))
	case "list_files", "list_dir":
		return exportToolLabel("ListDir", value("path"))
	case "grep", "hashline_grep", "search_files":
		return exportToolLabel("Search", value("pattern", "query"))
	case "shell":
		return exportToolLabel("Execute", value("command"))
	case "web_fetch":
		return exportToolLabel("WebFetch", value("url"))
	case "web_search":
		return exportToolLabel("WebSearch", value("query"))
	default:
		return "Tool: " + tool.Name
	}
}

func exportToolLabel(kind, value string) string {
	if value == "" {
		return kind
	}
	return fmt.Sprintf("%s: %s", kind, value)
}
