package tools

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

type markdownRenderer struct {
	output       bytes.Buffer
	base         *url.URL
	pendingSpace bool
}

var markdownEscaper = strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]")

func htmlToMarkdown(data []byte, base *url.URL) (string, error) {
	document, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("parse HTML: %w", err)
	}
	renderer := &markdownRenderer{base: base}
	renderer.render(document)
	return strings.TrimSpace(renderer.output.String()), nil
}

func (r *markdownRenderer) render(node *html.Node) {
	if node.Type == html.TextNode {
		r.text(node.Data)
		return
	}
	if node.Type != html.ElementNode {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			r.render(child)
		}
		return
	}
	tag := strings.ToLower(node.Data)
	switch tag {
	case "script", "style", "noscript", "svg", "iframe", "object", "embed", "head":
		return
	case "br":
		r.newline(1)
		return
	case "hr":
		r.newline(2)
		r.output.WriteString("---")
		r.newline(2)
		return
	case "img":
		source := r.link(attribute(node, "src"))
		if source != "" && !strings.HasPrefix(source, "data:") {
			r.flushSpace()
			fmt.Fprintf(&r.output, "![%s](%s)", escapeMarkdown(attribute(node, "alt")), source)
		}
		return
	case "pre":
		r.newline(2)
		r.output.WriteString("```\n")
		r.output.WriteString(strings.TrimSpace(rawText(node)))
		r.output.WriteString("\n```")
		r.newline(2)
		return
	case "code":
		r.flushSpace()
		r.output.WriteByte('`')
		r.output.WriteString(strings.TrimSpace(rawText(node)))
		r.output.WriteByte('`')
		return
	case "a":
		target := r.link(attribute(node, "href"))
		if target == "" {
			r.children(node)
			return
		}
		r.flushSpace()
		r.output.WriteByte('[')
		r.children(node)
		trailingSpace := r.pendingSpace
		r.pendingSpace = false
		fmt.Fprintf(&r.output, "](%s)", target)
		r.pendingSpace = trailingSpace
		return
	case "strong", "b":
		r.wrap(node, "**")
		return
	case "em", "i":
		r.wrap(node, "*")
		return
	case "del", "s", "strike":
		r.wrap(node, "~~")
		return
	case "h1", "h2", "h3", "h4", "h5", "h6":
		r.newline(2)
		level := int(tag[1] - '0')
		r.output.WriteString(strings.Repeat("#", level) + " ")
		r.children(node)
		r.newline(2)
		return
	case "li":
		r.newline(1)
		indent := strings.Repeat("  ", listDepth(node)-1)
		prefix := "- "
		if node.Parent != nil && strings.EqualFold(node.Parent.Data, "ol") {
			prefix = fmt.Sprintf("%d. ", listIndex(node))
		}
		r.output.WriteString(indent + prefix)
		r.children(node)
		r.newline(1)
		return
	case "blockquote":
		r.newline(2)
		r.output.WriteString("> ")
		r.children(node)
		r.newline(2)
		return
	case "th", "td":
		r.output.WriteString(" | ")
		r.children(node)
		return
	}
	block := tag == "p" || tag == "div" || tag == "section" || tag == "article" || tag == "main" || tag == "header" || tag == "footer" || tag == "nav" || tag == "table" || tag == "tr" || tag == "ul" || tag == "ol" || tag == "dl" || tag == "dt" || tag == "dd"
	if block {
		r.newline(2)
	}
	r.children(node)
	if block {
		r.newline(2)
	}
}

func (r *markdownRenderer) children(node *html.Node) {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		r.render(child)
	}
}

func (r *markdownRenderer) wrap(node *html.Node, marker string) {
	r.flushSpace()
	r.output.WriteString(marker)
	r.children(node)
	trailingSpace := r.pendingSpace
	r.pendingSpace = false
	r.output.WriteString(marker)
	r.pendingSpace = trailingSpace
}

func (r *markdownRenderer) text(value string) {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		if strings.IndexFunc(value, unicode.IsSpace) >= 0 {
			r.pendingSpace = true
		}
		return
	}
	r.flushSpace()
	r.output.WriteString(escapeMarkdown(strings.Join(fields, " ")))
	last, _ := utf8.DecodeLastRuneInString(value)
	r.pendingSpace = unicode.IsSpace(last)
}

func (r *markdownRenderer) flushSpace() {
	if r.pendingSpace && r.output.Len() > 0 {
		last := r.output.Bytes()[r.output.Len()-1]
		if last != ' ' && last != '\n' {
			r.output.WriteByte(' ')
		}
	}
	r.pendingSpace = false
}

func (r *markdownRenderer) newline(count int) {
	r.pendingSpace = false
	data := r.output.Bytes()
	for len(data) > 0 && (data[len(data)-1] == ' ' || data[len(data)-1] == '\t') {
		data = data[:len(data)-1]
	}
	r.output.Truncate(len(data))
	trailing := 0
	for index := len(data) - 1; index >= 0 && data[index] == '\n'; index-- {
		trailing++
	}
	for trailing < count {
		r.output.WriteByte('\n')
		trailing++
	}
}

func (r *markdownRenderer) link(raw string) string {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || raw == "" || (parsed.Scheme != "" && parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "mailto") {
		return ""
	}
	if r.base != nil {
		parsed = r.base.ResolveReference(parsed)
	}
	return parsed.String()
}

func attribute(node *html.Node, name string) string {
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, name) {
			return attribute.Val
		}
	}
	return ""
}

func rawText(node *html.Node) string {
	var output strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			output.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return output.String()
}

func listDepth(node *html.Node) int {
	depth := 0
	for parent := node.Parent; parent != nil; parent = parent.Parent {
		if parent.Data == "ul" || parent.Data == "ol" {
			depth++
		}
	}
	return max(depth, 1)
}

func listIndex(node *html.Node) int {
	index := 1
	for sibling := node.PrevSibling; sibling != nil; sibling = sibling.PrevSibling {
		if sibling.Type == html.ElementNode && sibling.Data == "li" {
			index++
		}
	}
	return index
}

func escapeMarkdown(value string) string {
	return markdownEscaper.Replace(value)
}
