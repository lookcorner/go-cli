package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type permissionRule struct {
	action  string
	pattern *regexp.Regexp
	raw     string
}

type RuleApprover struct {
	base  Approver
	allow []permissionRule
	deny  []permissionRule
}

// NewRuleApprover applies Gork-style Tool(pattern) rules around another
// approver. Deny rules take precedence, then allow rules, then the base mode.
func NewRuleApprover(base Approver, allow, deny []string) (*RuleApprover, error) {
	if base == nil {
		return nil, errors.New("base approver is required")
	}
	result := &RuleApprover{base: base}
	var err error
	if result.allow, err = compilePermissionRules(allow); err != nil {
		return nil, fmt.Errorf("invalid allow rule: %w", err)
	}
	if result.deny, err = compilePermissionRules(deny); err != nil {
		return nil, fmt.Errorf("invalid deny rule: %w", err)
	}
	return result, nil
}

func (a *RuleApprover) Approve(ctx context.Context, action, detail string) error {
	for _, rule := range a.deny {
		if rule.matches(action, detail) {
			return fmt.Errorf("permission denied by rule %s", rule.raw)
		}
	}
	for _, rule := range a.allow {
		if rule.matches(action, detail) {
			return nil
		}
	}
	return a.base.Approve(ctx, action, detail)
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
	if strings.EqualFold(r.action, "Bash") {
		switch action {
		case "shell", "run terminal command", "start background command":
			return r.pattern.MatchString(strings.TrimSpace(detail))
		default:
			return false
		}
	}
	return strings.EqualFold(strings.ReplaceAll(r.action, "_", " "), strings.ReplaceAll(action, "_", " ")) && r.pattern.MatchString(detail)
}
