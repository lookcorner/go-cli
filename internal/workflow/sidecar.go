package workflow

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// lookPath is os/exec.LookPath, overridable in tests.
var lookPath = exec.LookPath

// commandContext builds exec.CommandContext; overridable in tests.
var commandContext = exec.CommandContext

// RunWithSidecar starts an external Rhai runner and serves host requests until outcome.
func RunWithSidecar(ctx context.Context, runnerPath string, script string, args map[string]string, host *Host) (OutcomeMessage, error) {
	if strings.TrimSpace(runnerPath) == "" {
		return OutcomeMessage{}, ErrRunnerUnavailable
	}
	if strings.TrimSpace(script) == "" {
		return OutcomeMessage{}, errors.New("empty workflow script")
	}
	if host == nil {
		host = &Host{}
	}

	cmd := commandContext(ctx, runnerPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return OutcomeMessage{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return OutcomeMessage{}, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return OutcomeMessage{}, fmt.Errorf("start workflow runner: %w", err)
	}

	argsRaw := json.RawMessage(`{}`)
	if len(args) > 0 {
		encoded, marshalErr := json.Marshal(args)
		if marshalErr != nil {
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return OutcomeMessage{}, marshalErr
		}
		argsRaw = encoded
	}
	start := StartMessage{
		Type:        MsgStart,
		Script:      script,
		Args:        argsRaw,
		AgentBudget: host.budget(),
	}
	host.agentBudget = start.AgentBudget
	if err := writeJSONLine(stdin, start); err != nil {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return OutcomeMessage{}, err
	}

	outcome, serveErr := serveSidecar(ctx, stdout, stdin, host)
	_ = stdin.Close()
	waitErr := cmd.Wait()
	if serveErr != nil {
		return OutcomeMessage{}, serveErr
	}
	if waitErr != nil && ctx.Err() == nil && outcome.Type == "" {
		return OutcomeMessage{}, fmt.Errorf("workflow runner exited: %w", waitErr)
	}
	return outcome, nil
}

func serveSidecar(ctx context.Context, stdout io.Reader, stdin io.Writer, host *Host) (OutcomeMessage, error) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return OutcomeMessage{}, err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			return OutcomeMessage{}, fmt.Errorf("workflow runner protocol: %w", err)
		}
		switch envelope.Type {
		case MsgHostReq:
			var req HostRequest
			if err := json.Unmarshal([]byte(line), &req); err != nil {
				return OutcomeMessage{}, fmt.Errorf("decode host_request: %w", err)
			}
			reply := host.HandleRequest(ctx, req)
			if err := writeJSONLine(stdin, reply); err != nil {
				return OutcomeMessage{}, err
			}
		case MsgHostNotify:
			var n HostNotify
			if err := json.Unmarshal([]byte(line), &n); err != nil {
				return OutcomeMessage{}, fmt.Errorf("decode host_notify: %w", err)
			}
			host.HandleNotify(n)
		case MsgOutcome:
			var outcome OutcomeMessage
			if err := json.Unmarshal([]byte(line), &outcome); err != nil {
				return OutcomeMessage{}, fmt.Errorf("decode outcome: %w", err)
			}
			return outcome, nil
		default:
			return OutcomeMessage{}, fmt.Errorf("workflow runner protocol: unknown type %q", envelope.Type)
		}
	}
	if err := scanner.Err(); err != nil {
		return OutcomeMessage{}, err
	}
	return OutcomeMessage{}, errors.New("workflow runner closed stdout without outcome")
}

func writeJSONLine(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

// Execute runs a resolved workflow via the external Rhai sidecar + host agent().
func Execute(ctx context.Context, resolved Resolved, args map[string]string, host *Host) (string, error) {
	if resolved.Source == "builtin" && strings.TrimSpace(resolved.Script) == "" {
		return "", ErrBuiltinScriptMissing
	}
	if err := ValidateResolved(resolved); err != nil {
		return "", err
	}
	runner, err := ResolveRunnerBinary()
	if err != nil {
		return "", err
	}
	if host == nil {
		host = &Host{}
	}
	outcome, err := RunWithSidecar(ctx, runner, resolved.Script, args, host)
	if err != nil {
		return "", err
	}
	return FormatOutcome(outcome)
}
