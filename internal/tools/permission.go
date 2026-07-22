package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

type ModeApprover struct {
	mu                  sync.RWMutex
	mode                PermissionMode
	prompt              Approver
	lockedDeny          bool
	alwaysApproveLocked bool
	autoModeLocked      bool
}

type permissionModeController interface {
	SetPermissionMode(PermissionMode) error
	PermissionMode() PermissionMode
}

type permissionBypassKey struct{}
type permissionClassifierKey struct{}

// PermissionClassifier decides whether one request may run without prompting.
type PermissionClassifier func(context.Context, string, string) (bool, error)

func WithPermissionBypass(ctx context.Context) context.Context {
	return context.WithValue(ctx, permissionBypassKey{}, true)
}

// PermissionBypassed reports whether prompt-based approval should be skipped.
func PermissionBypassed(ctx context.Context) bool {
	bypassed, _ := ctx.Value(permissionBypassKey{}).(bool)
	return bypassed
}

// WithPermissionClassifier attaches the session's live classifier to one tool request.
func WithPermissionClassifier(ctx context.Context, classifier PermissionClassifier) context.Context {
	return context.WithValue(ctx, permissionClassifierKey{}, classifier)
}

// ClassifyPermission returns available=false when no live classifier produced a verdict.
func ClassifyPermission(ctx context.Context, action, detail string) (allowed, available bool) {
	classifier, _ := ctx.Value(permissionClassifierKey{}).(PermissionClassifier)
	if classifier == nil {
		return false, false
	}
	allowed, err := classifier(ctx, action, detail)
	return allowed, err == nil
}

func NewModeApprover(mode PermissionMode, prompt Approver) (*ModeApprover, error) {
	return NewModeApproverWithLocks(mode, prompt, false, false)
}

func NewModeApproverWithAutoLock(mode PermissionMode, prompt Approver, autoLocked bool) (*ModeApprover, error) {
	return NewModeApproverWithLocks(mode, prompt, autoLocked, false)
}

func NewModeApproverWithLocks(mode PermissionMode, prompt Approver, alwaysApproveLocked, autoModeLocked bool) (*ModeApprover, error) {
	if mode != PermissionPrompt && mode != PermissionAuto && mode != PermissionAlwaysApprove && mode != PermissionDeny {
		return nil, fmt.Errorf("unknown permission mode %q", mode)
	}
	if alwaysApproveLocked && mode == PermissionAlwaysApprove || autoModeLocked && mode == PermissionAuto {
		mode = PermissionPrompt
	}
	if (mode == PermissionPrompt || mode == PermissionAuto) && prompt == nil {
		return nil, errors.New("prompt approver is required")
	}
	return &ModeApprover{
		mode: mode, prompt: prompt, lockedDeny: mode == PermissionDeny,
		alwaysApproveLocked: alwaysApproveLocked, autoModeLocked: autoModeLocked,
	}, nil
}

func (a *ModeApprover) Approve(ctx context.Context, action, detail string) error {
	a.mu.RLock()
	mode, prompt := a.mode, a.prompt
	a.mu.RUnlock()
	switch mode {
	case PermissionAlwaysApprove:
		return nil
	case PermissionDeny:
		return &PermissionDeniedError{Action: action}
	case PermissionPrompt, PermissionAuto:
		if PermissionBypassed(ctx) {
			return nil
		}
		if mode == PermissionAuto && AutoModeFastPath(action, detail) {
			return nil
		}
		if mode == PermissionAuto {
			if allowed, available := ClassifyPermission(ctx, action, detail); available {
				if allowed {
					return nil
				}
				return prompt.Approve(ctx, action, detail)
			}
			if AutoModeAllows(action, detail) {
				return nil
			}
		}
		return prompt.Approve(ctx, action, detail)
	default:
		return fmt.Errorf("unknown permission mode %q", mode)
	}
}

func (a *ModeApprover) SetPermissionMode(mode PermissionMode) error {
	if mode != PermissionPrompt && mode != PermissionAuto && mode != PermissionAlwaysApprove && mode != PermissionDeny {
		return fmt.Errorf("unknown permission mode %q", mode)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lockedDeny && mode != PermissionDeny {
		return errors.New("permission mode is locked to deny")
	}
	if a.alwaysApproveLocked && mode == PermissionAlwaysApprove {
		return errors.New("always-approve is disabled by managed policy")
	}
	if a.autoModeLocked && mode == PermissionAuto {
		return errors.New("auto permission mode is disabled")
	}
	if (mode == PermissionPrompt || mode == PermissionAuto) && a.prompt == nil {
		return errors.New("prompt approver is required")
	}
	a.mode = mode
	return nil
}

func (a *ModeApprover) PermissionMode() PermissionMode {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.mode
}

// AutoModeAllows is the deterministic fallback heuristic for auto mode.
// Unknown or externally-visible actions return false and still prompt.
func AutoModeAllows(action, detail string) bool {
	if AutoModeFastPath(action, detail) {
		return true
	}
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "shell", "run terminal command", "start background command":
		return routineShellCommand(detail)
	default:
		return false
	}
}

// AutoModeFastPath allows actions that never need transcript classification.
func AutoModeFastPath(action, detail string) bool {
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "write_file", "edit_file", "read policy", "grep policy", "web search",
		"enter plan mode", "exit plan mode":
		return true
	case "shell", "run terminal command", "start background command":
		switch strings.TrimSpace(detail) {
		case "true", "false", ":":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func routineShellCommand(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	lower := strings.ToLower(command)
	if strings.ContainsAny(command, "|<>") || strings.Contains(strings.ReplaceAll(command, "&&", ""), "&") {
		return false
	}
	for _, unsafe := range []string{
		"$(", "`", "sudo ", "git push", "git reset", "git rebase",
		"git clean", "git branch -d", "git branch --delete", "cargo publish", "npm publish", "pnpm publish",
		"yarn publish", "kubectl apply", "kubectl delete", "kubectl exec", "ssh ", "scp ",
		"rsync ", "curl ", "wget ", "rm ", "rmdir ", "mkfs", "dd if=", "shutdown",
		"reboot", "chmod ", "chown ", "kill ", "pkill ", "base64 -d",
	} {
		if strings.Contains(lower, unsafe) {
			return false
		}
	}
	parts := strings.FieldsFunc(strings.ReplaceAll(lower, "&&", "\n"), func(r rune) bool { return r == ';' || r == '\n' })
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if !routineShellSegment(strings.TrimSpace(part)) {
			return false
		}
	}
	return true
}

func routineShellSegment(command string) bool {
	for _, prefix := range []string{
		"cargo ", "go ", "pytest", "python ", "python3 ", "node ", "rustc ", "rustfmt", "clippy",
		"make ", "cmake ", "bazel ", "just ", "git status", "git diff", "git log", "git branch",
		"git add", "git commit", "git checkout", "git switch", "git stash", "git pull", "git fetch",
		"git show", "git blame", "git grep", "git ls-files", "git rev-parse", "git describe",
		"git merge-base", "git worktree list", "kubectl get", "kubectl logs", "kubectl describe",
		"cd", "pushd", "popd", "ls", "pwd", "echo ", "printf ", "cat ", "head ", "tail ",
		"wc ", "rg ", "grep ", "which ", "type ", "sort ", "uniq ", "tr ", "cut ", "diff ",
		"jq ", "date", "whoami", "hostname", "uname", "nproc", "printenv", "stat ", "file ",
		"tree", "basename ", "dirname ", "realpath ", "readlink ", "strings ", "sleep ", "df ",
		"du ", "ps ", "top", "htop", "set", "true", "false", ":",
	} {
		bare := strings.TrimSpace(prefix)
		if command == bare || strings.HasSuffix(prefix, " ") && strings.HasPrefix(command, prefix) ||
			!strings.HasSuffix(prefix, " ") && strings.HasPrefix(command, bare+" ") {
			return true
		}
	}
	return false
}

type permissionRule struct {
	action  string
	pattern *regexp.Regexp
	raw     string
}

type PermissionDeniedError struct {
	Action string
	Reason string
}

func (e *PermissionDeniedError) Error() string {
	if e.Reason != "" {
		return e.Reason
	}
	return fmt.Sprintf("permission denied for %s", e.Action)
}

func IsPermissionDenied(err error) bool {
	var denied *PermissionDeniedError
	return errors.As(err, &denied)
}

type RuleApprover struct {
	base  Approver
	asker Approver
	allow []permissionRule
	ask   []permissionRule
	deny  []permissionRule
}

// NewRuleApprover applies Gork-style Tool(pattern) rules around another
// approver. Deny rules take precedence, then allow rules, then the base mode.
func NewRuleApprover(base Approver, allow, deny []string) (*RuleApprover, error) {
	if base == nil {
		return nil, errors.New("base approver is required")
	}
	return NewPolicyApprover(base, base, allow, nil, deny)
}

// NewPolicyApprover adds explicit ask rules. Matching order is deny, ask,
// allow, then the configured base approval mode.
func NewPolicyApprover(base, asker Approver, allow, ask, deny []string) (*RuleApprover, error) {
	if base == nil || asker == nil {
		return nil, errors.New("base and ask approvers are required")
	}
	result := &RuleApprover{base: base, asker: asker}
	var err error
	if result.allow, err = compilePermissionRules(allow); err != nil {
		return nil, fmt.Errorf("invalid allow rule: %w", err)
	}
	if result.deny, err = compilePermissionRules(deny); err != nil {
		return nil, fmt.Errorf("invalid deny rule: %w", err)
	}
	if result.ask, err = compilePermissionRules(ask); err != nil {
		return nil, fmt.Errorf("invalid ask rule: %w", err)
	}
	return result, nil
}

func (a *RuleApprover) Approve(ctx context.Context, action, detail string) error {
	for _, rule := range a.deny {
		if rule.matches(action, detail) {
			return &PermissionDeniedError{Action: action, Reason: fmt.Sprintf("permission denied by rule %s", rule.raw)}
		}
	}
	if PermissionBypassed(ctx) {
		return a.base.Approve(ctx, action, detail)
	}
	for _, rule := range a.ask {
		if rule.matches(action, detail) {
			return a.asker.Approve(ctx, action, detail)
		}
	}
	for _, rule := range a.allow {
		if rule.matches(action, detail) {
			return nil
		}
	}
	return a.base.Approve(ctx, action, detail)
}

func (a *RuleApprover) SetPermissionMode(mode PermissionMode) error {
	controller, ok := a.base.(permissionModeController)
	if !ok {
		return errors.New("permission mode cannot be changed")
	}
	return controller.SetPermissionMode(mode)
}

func (a *RuleApprover) PermissionMode() PermissionMode {
	controller, ok := a.base.(permissionModeController)
	if !ok {
		return ""
	}
	return controller.PermissionMode()
}

func compilePermissionRules(values []string) ([]permissionRule, error) {
	rules := make([]permissionRule, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, errors.New("rule must not be empty")
		}
		action, pattern := "Bash", value
		if open := strings.IndexByte(value, '('); open >= 0 {
			if !strings.HasSuffix(value, ")") || open == 0 {
				return nil, fmt.Errorf("%q must use Tool(pattern) syntax", value)
			}
			action = strings.TrimSpace(value[:open])
			pattern = value[open+1 : len(value)-1]
		}
		if action == "" || pattern == "" {
			return nil, fmt.Errorf("%q has an empty tool or pattern", value)
		}
		expression := "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$"
		compiled, err := regexp.Compile(expression)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", value, err)
		}
		rules = append(rules, permissionRule{action: action, pattern: compiled, raw: value})
	}
	return rules, nil
}

func (r permissionRule) matches(action, detail string) bool {
	if strings.EqualFold(r.action, "Any") {
		return r.pattern.MatchString(detail)
	}
	if strings.EqualFold(r.action, "Bash") {
		switch action {
		case "shell", "run terminal command", "start background command":
			return r.pattern.MatchString(strings.TrimSpace(detail))
		default:
			return false
		}
	}
	if strings.EqualFold(r.action, "Edit") {
		switch action {
		case "write_file", "edit_file", "search_replace":
			return r.pattern.MatchString(detail)
		}
		return false
	}
	if strings.EqualFold(r.action, "Mcp") {
		return action == "MCP tool" && r.pattern.MatchString(detail)
	}
	if strings.EqualFold(r.action, "Read") {
		return action == "read policy" && r.pattern.MatchString(detail)
	}
	if strings.EqualFold(r.action, "Grep") {
		return action == "grep policy" && r.pattern.MatchString(detail)
	}
	if strings.EqualFold(r.action, "WebFetch") {
		return action == "web fetch" && r.pattern.MatchString(detail)
	}
	if strings.EqualFold(r.action, "WebFetchDomain") {
		if action != "web fetch" {
			return false
		}
		parsed, err := url.Parse(detail)
		return err == nil && r.pattern.MatchString(parsed.Hostname())
	}
	return strings.EqualFold(strings.ReplaceAll(r.action, "_", " "), strings.ReplaceAll(action, "_", " ")) && r.pattern.MatchString(detail)
}
