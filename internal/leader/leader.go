package leader

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ProtocolVersion = 1
	maxMessageBytes = 64 << 20
)

type State string

const (
	StateReachable           State = "Reachable"
	StateStale               State = "Stale"
	StateUnreachable         State = "Unreachable"
	StateUnsupportedProtocol State = "UnsupportedProtocol"
)

type Info struct {
	PID                   uint32   `json:"pid"`
	SocketPath            string   `json:"socket_path"`
	LockPath              string   `json:"lock_path"`
	WSURLSuffix           string   `json:"ws_url_suffix"`
	LeaderProtocolVersion uint32   `json:"leader_protocol_version"`
	LeaderBinaryVersion   string   `json:"leader_binary_version"`
	ProfilingSupported    bool     `json:"profiling_supported"`
	ProfilingCompiledIn   bool     `json:"profiling_compiled_in"`
	CPUProfileActive      bool     `json:"cpu_profile_active"`
	CPUProfileStopping    bool     `json:"cpu_profile_stopping"`
	ProfileStartedAt      *string  `json:"profile_started_at"`
	ProfileFormats        []string `json:"profile_formats"`
}

type Descriptor struct {
	PIDFromLock *uint32
	LockPath    string
	SocketPath  string
	WSURLSuffix string
	State       State
	ClientID    uint64
	LiveInfo    *Info
}

func (d Descriptor) PID() *uint32 {
	if d.LiveInfo != nil {
		value := d.LiveInfo.PID
		return &value
	}
	return d.PIDFromLock
}

type QueryResult struct {
	ClientID uint64
	Info     Info
}

type queryFunc func(context.Context, string) (QueryResult, error)

func Discover(ctx context.Context, root string) ([]Descriptor, error) {
	return discover(ctx, root, query)
}

func discover(ctx context.Context, root string, query queryFunc) ([]Descriptor, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read leader directory: %w", err)
	}
	type paths struct{ lock, socket string }
	candidates := make(map[string]paths)
	for _, entry := range entries {
		name := entry.Name()
		suffix, kind, ok := leaderFile(name)
		if !ok {
			continue
		}
		current := candidates[suffix]
		if kind == "lock" {
			current.lock = filepath.Join(root, name)
		} else {
			current.socket = filepath.Join(root, name)
		}
		candidates[suffix] = current
	}
	suffixes := make([]string, 0, len(candidates))
	for suffix := range candidates {
		suffixes = append(suffixes, suffix)
	}
	sort.Strings(suffixes)

	result := make([]Descriptor, 0, len(suffixes))
	for _, suffix := range suffixes {
		paths := candidates[suffix]
		descriptor := Descriptor{
			PIDFromLock: readPID(paths.lock), LockPath: paths.lock, SocketPath: paths.socket,
			WSURLSuffix: suffix, State: StateStale,
		}
		if paths.socket != "" {
			queryCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
			live, queryErr := query(queryCtx, paths.socket)
			cancel()
			switch {
			case queryErr == nil:
				descriptor.State, descriptor.ClientID, descriptor.LiveInfo = StateReachable, live.ClientID, &live.Info
			case errors.Is(queryErr, ErrUnsupportedProtocol):
				descriptor.State = StateUnsupportedProtocol
			default:
				descriptor.State = StateUnreachable
			}
		}
		result = append(result, descriptor)
	}
	return result, nil
}

func Select(descriptors []Descriptor, pid *uint32) (Descriptor, error) {
	var matches []Descriptor
	for _, descriptor := range descriptors {
		if descriptor.State != StateReachable {
			continue
		}
		if pid != nil {
			current := descriptor.PID()
			if current == nil || *current != *pid {
				continue
			}
		} else if descriptor.WSURLSuffix != "" {
			continue
		}
		matches = append(matches, descriptor)
	}
	if len(matches) == 0 {
		if pid != nil {
			return Descriptor{}, fmt.Errorf("no reachable leader found for pid %d", *pid)
		}
		return Descriptor{}, errors.New("no reachable production leader found")
	}
	if len(matches) > 1 {
		return Descriptor{}, errors.New("multiple reachable leader candidates matched target")
	}
	return matches[0], nil
}

var ErrUnsupportedProtocol = errors.New("unsupported leader protocol")

func leaderFile(name string) (suffix, kind string, ok bool) {
	if !strings.HasPrefix(name, "leader") {
		return "", "", false
	}
	for _, extension := range []string{".lock", ".sock"} {
		if strings.HasSuffix(name, extension) {
			return strings.TrimSuffix(strings.TrimPrefix(name, "leader"), extension), strings.TrimPrefix(extension, "."), true
		}
	}
	return "", "", false
}

func readPID(path string) *uint32 {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
	if err != nil || value == 0 {
		return nil
	}
	pid := uint32(value)
	return &pid
}

func writeMessage(writer io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > maxMessageBytes {
		return errors.New("leader message is too large")
	}
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(data)))
	if _, err := writer.Write(length[:]); err != nil {
		return err
	}
	_, err = writer.Write(data)
	return err
}

func readMessage(reader io.Reader, value any) error {
	var length [4]byte
	if _, err := io.ReadFull(reader, length[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(length[:])
	if size > maxMessageBytes {
		return errors.New("leader message is too large")
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(reader, data); err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func queryStream(ctx context.Context, stream io.ReadWriteCloser) (QueryResult, error) {
	defer stream.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if connection, ok := stream.(interface{ SetDeadline(time.Time) error }); ok {
			_ = connection.SetDeadline(deadline)
		}
	}
	register := map[string]any{
		"type": "register", "client_type": "gork-go-leader-cli", "mode": "stdio",
		"capabilities": map[string]any{},
	}
	if err := writeMessage(stream, register); err != nil {
		return QueryResult{}, err
	}
	var registered struct {
		Type                  string  `json:"type"`
		ClientID              uint64  `json:"client_id"`
		Ready                 *bool   `json:"ready"`
		LeaderProtocolVersion *uint32 `json:"leader_protocol_version"`
		LeaderCapabilities    *struct {
			ControlV1 bool `json:"control_v1"`
		} `json:"leader_capabilities"`
		Message string `json:"message"`
	}
	if err := readMessage(stream, &registered); err != nil {
		return QueryResult{}, err
	}
	if registered.Type == "error" {
		return QueryResult{}, errors.New(registered.Message)
	}
	if registered.Type != "registered" {
		return QueryResult{}, fmt.Errorf("unexpected leader registration response %q", registered.Type)
	}
	if registered.Ready != nil && !*registered.Ready {
		var ready struct {
			Type string `json:"type"`
		}
		if err := readMessage(stream, &ready); err != nil {
			return QueryResult{}, err
		}
		if ready.Type != "leader_ready" {
			return QueryResult{}, fmt.Errorf("unexpected leader readiness response %q", ready.Type)
		}
	}
	if registered.LeaderProtocolVersion == nil || *registered.LeaderProtocolVersion != ProtocolVersion ||
		registered.LeaderCapabilities == nil || !registered.LeaderCapabilities.ControlV1 {
		return QueryResult{}, ErrUnsupportedProtocol
	}
	if err := writeMessage(stream, map[string]any{
		"type": "control", "request_id": "1", "command": map[string]any{"type": "get_leader_info"},
	}); err != nil {
		return QueryResult{}, err
	}
	var response struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Result    struct {
			OK  *Info `json:"Ok"`
			Err any   `json:"Err"`
		} `json:"result"`
	}
	if err := readMessage(stream, &response); err != nil {
		return QueryResult{}, err
	}
	if response.Type != "control_result" || response.RequestID != "1" || response.Result.OK == nil {
		return QueryResult{}, errors.New("leader rejected GetLeaderInfo")
	}
	return QueryResult{ClientID: registered.ClientID, Info: *response.Result.OK}, nil
}
