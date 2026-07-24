package main

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

type completionSpec struct {
	flags    []string
	children map[string]completionSpec
}

var completionRoot = completionSpec{
	flags: []string{
		"--acp", "--allow", "--approval", "--backend", "--base-url", "--config",
		"--deny", "--experimental-memory", "--fullscreen", "--goal", "--goal-runs",
		"--interactive", "--max-steps", "--minimal", "--model", "--no-memory",
		"--previous-response-id", "--resume", "--sandbox", "--session-dir",
		"--system", "--timeout", "--trust", "--tui", "--version", "--workspace",
	},
	children: map[string]completionSpec{
		"completions": {children: leaves("bash", "elvish", "fish", "powershell", "zsh")},
		"export":      {flags: []string{"--clipboard", "-c"}},
		"inspect":     {flags: words("--config --json")},
		"login":       {flags: words("--auth-file --audience --client-id --config --device-auth --issuer --no-browser --oauth --scopes")},
		"logout":      {flags: words("--auth-file --client-id --config --issuer")},
		"mcp": {children: map[string]completionSpec{
			"add":    {flags: words("--config --env --header --scope --transport -H -e -s -t")},
			"doctor": {flags: words("--config --json")}, "list": {flags: words("--config --json")},
			"remove": {flags: words("--config --scope -s")},
		}},
		"memory": {children: map[string]completionSpec{"clear": {flags: words("--all --global --workspace --yes -y")}}},
		"models": {flags: []string{"--config"}},
		"plugin": {children: map[string]completionSpec{
			"install": {}, "list": {}, "update": {}, "uninstall": {flags: words("--confirm --keep-data")},
			"marketplace": {children: map[string]completionSpec{
				"add": {}, "list": {flags: []string{"--json"}}, "remove": {}, "update": {},
			}},
		}},
		"sessions": {children: map[string]completionSpec{
			"delete": {}, "list": {flags: words("--limit -n")}, "search": {flags: words("--limit -n")},
		}},
		"setup": {flags: words("--config --json")},
		"worktree": {children: map[string]completionSpec{
			"db": {children: leaves("path", "rebuild", "stats")},
			"gc": {flags: words("--dry-run --force --max-age -f")}, "prune": {flags: words("--dry-run --force --max-age -f")},
			"list": {flags: words("--all --json --repo --type")}, "ls": {flags: words("--all --json --repo --type")},
			"rm": {flags: words("--dry-run --force -f")}, "show": {},
		}},
	},
}

var completionValueFlags = map[string]bool{
	"--allow": true, "--approval": true, "--auth-file": true, "--audience": true,
	"--backend": true, "--base-url": true, "--client-id": true, "--config": true,
	"--deny": true, "--goal-runs": true, "--issuer": true, "--limit": true,
	"--max-age": true, "--max-steps": true, "--model": true, "--previous-response-id": true,
	"--env": true, "--header": true, "--repo": true, "--resume": true, "--sandbox": true, "--scope": true, "--scopes": true,
	"--session-dir": true, "--system": true, "--timeout": true, "--type": true,
	"--transport": true, "--workspace": true, "-H": true, "-e": true, "-n": true, "-s": true, "-t": true,
}

func words(value string) []string { return strings.Fields(value) }

func leaves(names ...string) map[string]completionSpec {
	result := make(map[string]completionSpec, len(names))
	for _, name := range names {
		result[name] = completionSpec{}
	}
	return result
}

func runCompletions(args []string, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		completionsUsage(stderr)
		return errors.New("completions requires a shell")
	}
	scripts := map[string]string{
		"bash": `_gork_completion() {
  COMPREPLY=($(gork __complete "${COMP_WORDS[@]:1:$COMP_CWORD}"))
}
complete -F _gork_completion gork
`,
		"zsh": `#compdef gork
_gork() {
  local -a choices
  choices=("${(@f)$(gork __complete ${words[2,CURRENT]})}")
  _describe 'gork' choices
}
compdef _gork gork
`,
		"fish": `complete -c gork -f -a '(gork __complete (commandline -opc)[2..-1] (commandline -ct))'
`,
		"powershell": `Register-ArgumentCompleter -Native -CommandName gork -ScriptBlock {
  param($wordToComplete, $commandAst, $cursorPosition)
  $args = @($commandAst.CommandElements | Select-Object -Skip 1 | ForEach-Object { $_.Extent.Text })
  gork __complete @args | ForEach-Object {
    [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
  }
}
`,
		"elvish": `set edit:completion:arg-completer[gork] = {|@words| gork __complete $@words }
`,
	}
	script, ok := scripts[args[0]]
	if !ok {
		completionsUsage(stderr)
		return fmt.Errorf("unsupported completion shell %q", cleanCLIText(args[0]))
	}
	_, err := io.WriteString(stdout, script)
	return err
}

func runCompletionQuery(args []string, output io.Writer) error {
	for _, choice := range completionCandidates(args) {
		fmt.Fprintln(output, choice)
	}
	return nil
}

func completionCandidates(args []string) []string {
	current := ""
	if len(args) > 0 {
		current = args[len(args)-1]
		args = args[:len(args)-1]
	}
	spec := completionRoot
	skipValue := false
	for _, arg := range args {
		if skipValue {
			skipValue = false
			continue
		}
		if completionValueFlags[arg] {
			skipValue = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if next, ok := spec.children[arg]; ok {
			spec = next
		}
	}
	if skipValue {
		return nil
	}
	choices := append([]string(nil), spec.flags...)
	for name := range spec.children {
		choices = append(choices, name)
	}
	sort.Strings(choices)
	return filterCompletionPrefix(choices, current)
}

func filterCompletionPrefix(choices []string, prefix string) []string {
	result := choices[:0]
	for _, choice := range choices {
		if strings.HasPrefix(choice, prefix) {
			result = append(result, choice)
		}
	}
	return result
}

func completionsUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: gork completions <bash|elvish|fish|powershell|zsh>")
}
