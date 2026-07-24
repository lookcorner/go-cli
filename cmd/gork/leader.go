package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"

	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/leader"
)

func runLeader(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		leaderUsage(stderr)
		return errors.New("leader requires list, info, or kill")
	}
	switch args[0] {
	case "list":
		flags := flag.NewFlagSet("gork leader list", flag.ContinueOnError)
		flags.SetOutput(stderr)
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("leader list does not accept positional arguments")
		}
		descriptors, err := discoverLeaders()
		if err != nil {
			return err
		}
		output := stdout
		if !*jsonOutput && len(descriptors) > 0 {
			output = stderr
		}
		return writeLeaderList(descriptors, *jsonOutput, output)
	case "info":
		flags := flag.NewFlagSet("gork leader info", flag.ContinueOnError)
		flags.SetOutput(stderr)
		var pid uint64
		flags.Uint64Var(&pid, "pid", 0, "leader process ID")
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || pid > uint64(^uint32(0)) {
			return errors.New("leader info accepts only --pid and --json")
		}
		descriptors, err := discoverLeaders()
		if err != nil {
			return err
		}
		var target *uint32
		if pid > 0 {
			value := uint32(pid)
			target = &value
		}
		descriptor, err := leader.Select(descriptors, target)
		if err != nil {
			return err
		}
		return writeLeaderInfo(descriptor, leader.QueryResult{ClientID: descriptor.ClientID, Info: *descriptor.LiveInfo}, *jsonOutput, stdout)
	case "kill":
		return errors.New("leader kill is not implemented")
	case "-h", "--help":
		leaderUsage(stderr)
		return nil
	default:
		leaderUsage(stderr)
		return fmt.Errorf("unknown leader command %q", cleanCLIText(args[0]))
	}
}

func discoverLeaders() ([]leader.Descriptor, error) {
	path, err := config.DefaultPath()
	if err != nil {
		return nil, err
	}
	return leader.Discover(context.Background(), filepath.Dir(path))
}

func writeLeaderList(descriptors []leader.Descriptor, jsonOutput bool, output io.Writer) error {
	if jsonOutput {
		values := make([]map[string]any, 0, len(descriptors))
		for _, descriptor := range descriptors {
			values = append(values, leaderDescriptorJSON(descriptor))
		}
		return json.NewEncoder(output).Encode(values)
	}
	if len(descriptors) == 0 {
		fmt.Fprintln(output, "No leader candidates found.")
		return nil
	}
	for _, descriptor := range descriptors {
		pid := "?"
		if value := descriptor.PID(); value != nil {
			pid = strconv.FormatUint(uint64(*value), 10)
		}
		socket := descriptor.SocketPath
		if socket == "" {
			socket = "?"
		}
		fmt.Fprintf(output, "  PID %s (%s) -- %s\n", pid, descriptor.State, socket)
	}
	return nil
}

func writeLeaderInfo(descriptor leader.Descriptor, result leader.QueryResult, jsonOutput bool, output io.Writer) error {
	if jsonOutput {
		value := leaderDescriptorJSON(descriptor)
		value["clientId"] = result.ClientID
		value["info"] = result.Info
		return json.NewEncoder(output).Encode(value)
	}
	info := result.Info
	fmt.Fprintln(output, "LeaderInfo {")
	fmt.Fprintf(output, "  pid: %d,\n", info.PID)
	fmt.Fprintf(output, "  socket_path: %q,\n", info.SocketPath)
	fmt.Fprintf(output, "  lock_path: %q,\n", info.LockPath)
	fmt.Fprintf(output, "  ws_url_suffix: %q,\n", info.WSURLSuffix)
	fmt.Fprintf(output, "  leader_protocol_version: %d,\n", info.LeaderProtocolVersion)
	fmt.Fprintf(output, "  leader_binary_version: %q,\n", info.LeaderBinaryVersion)
	fmt.Fprintf(output, "  profiling_supported: %t,\n", info.ProfilingSupported)
	fmt.Fprintf(output, "  profiling_compiled_in: %t,\n", info.ProfilingCompiledIn)
	fmt.Fprintf(output, "  cpu_profile_active: %t,\n", info.CPUProfileActive)
	fmt.Fprintf(output, "  cpu_profile_stopping: %t,\n", info.CPUProfileStopping)
	fmt.Fprintln(output, "}")
	return nil
}

func leaderDescriptorJSON(descriptor leader.Descriptor) map[string]any {
	var livePID any
	if descriptor.LiveInfo != nil {
		livePID = descriptor.LiveInfo.PID
	}
	return map[string]any{
		"pid":            descriptor.PID(),
		"pidFromLock":    descriptor.PIDFromLock,
		"pidLive":        livePID,
		"classification": descriptor.State,
		"socketPath":     optionalLeaderPath(descriptor.SocketPath),
		"lockPath":       optionalLeaderPath(descriptor.LockPath),
		"wsUrlSuffix":    descriptor.WSURLSuffix,
	}
}

func optionalLeaderPath(path string) any {
	if path == "" {
		return nil
	}
	return path
}

func leaderUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: gork leader <list|info|kill> [--json] [--pid PID]")
}
