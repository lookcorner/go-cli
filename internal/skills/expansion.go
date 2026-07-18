package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type skillReference struct {
	name  string
	args  string
	skill Skill
	start int
	end   int
}

// ExpandReferences loads user-invocable /skill references from text.
func (c *Catalog) ExpandReferences(text, sessionID string) string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	available := make(map[string]Skill, len(c.byName))
	for name, skill := range c.byName {
		if skill.UserInvocable && !c.disabled[name] {
			available[name] = skill
		}
	}
	c.mu.RUnlock()
	refs := parseSkillReferences(text, available)
	if len(refs) == 0 {
		return ""
	}
	var blocks []string
	for _, ref := range refs {
		data, err := os.ReadFile(ref.skill.Path)
		if err != nil || len(data) > maxSkillBytes || !utf8.Valid(data) {
			continue
		}
		body := substituteSkillArguments(string(data), ref.args, filepath.Dir(ref.skill.Path), sessionID)
		if ref.args == "" {
			blocks = append(blocks, fmt.Sprintf("<skill name=\"%s\">\n%s\n</skill>", ref.name, body))
		} else {
			blocks = append(blocks, fmt.Sprintf("<skill name=\"%s\" args=\"%s\">\n%s\n</skill>", ref.name, ref.args, body))
		}
	}
	if len(blocks) == 0 {
		return ""
	}
	var output strings.Builder
	output.WriteString("<skill_information>\n<skills_referenced>\n")
	seen := make(map[string]bool)
	for _, ref := range refs {
		key := ref.name + "\x00" + ref.skill.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		fmt.Fprintf(&output, "<skill name=\"%s\" path=\"%s\"/>\n", ref.name, ref.skill.Path)
	}
	output.WriteString("</skills_referenced>\n")
	output.WriteString(strings.Join(blocks, "\n"))
	output.WriteString("\n</skill_information>")
	return output.String()
}

func parseSkillReferences(text string, available map[string]Skill) []skillReference {
	text = strings.TrimSpace(text)
	var refs []skillReference
	for index := 0; index < len(text); {
		if text[index] != '/' || index > 0 && !isASCIISpace(text[index-1]) {
			index++
			continue
		}
		end := index + 1
		for end < len(text) && !isASCIISpace(text[end]) {
			end++
		}
		name := text[index+1 : end]
		skill, ok := available[name]
		if !ok {
			index = end
			continue
		}
		refs = append(refs, skillReference{name: name, skill: skill, start: index, end: end})
		index = end
	}
	for index := range refs {
		end := len(text)
		if index+1 < len(refs) {
			end = refs[index+1].start
		}
		refs[index].args = strings.TrimSpace(text[refs[index].end:end])
	}
	return refs
}

func isASCIISpace(char byte) bool {
	return char == ' ' || char == '\t' || char == '\n' || char == '\r'
}

func substituteSkillArguments(body, args, skillDir, sessionID string) string {
	argv := strings.Fields(args)
	usedArgs := false
	for index := len(argv) + 20; index >= 0; index-- {
		value := ""
		if index < len(argv) {
			value = argv[index]
		}
		indexed := fmt.Sprintf("$ARGUMENTS[%d]", index)
		if strings.Contains(body, indexed) {
			body = strings.ReplaceAll(body, indexed, value)
			usedArgs = true
		}
		var replaced bool
		body, replaced = replaceShortArgument(body, fmt.Sprintf("$%d", index), value)
		usedArgs = usedArgs || replaced
	}
	if strings.Contains(body, "$ARGUMENTS") {
		body = strings.ReplaceAll(body, "$ARGUMENTS", args)
		usedArgs = true
	}
	body = strings.ReplaceAll(body, "${SKILL_DIR}", skillDir)
	body = strings.ReplaceAll(body, "${CLAUDE_SKILL_DIR}", skillDir)
	if sessionID != "" {
		body = strings.ReplaceAll(body, "${SESSION_ID}", sessionID)
		body = strings.ReplaceAll(body, "${CLAUDE_SESSION_ID}", sessionID)
	}
	if args != "" && !usedArgs {
		body += "\n\n**ARGUMENTS:** " + args
	}
	return body
}

func replaceShortArgument(body, token, value string) (string, bool) {
	var output strings.Builder
	replaced := false
	for {
		index := strings.Index(body, token)
		if index < 0 {
			output.WriteString(body)
			return output.String(), replaced
		}
		output.WriteString(body[:index])
		rest := body[index+len(token):]
		if len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
			output.WriteString(token)
		} else {
			output.WriteString(value)
			replaced = true
		}
		body = rest
	}
}
