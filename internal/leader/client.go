package leader

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

type Registration struct {
	ClientType   string
	Capabilities ClientCapabilities
}

type SpawnFunc func() error

type Client struct {
	stream io.ReadWriteCloser
	mu     sync.Mutex
}

func ConnectOrSpawn(ctx context.Context, socketPath string, registration Registration, spawn SpawnFunc) (*Client, error) {
	return connectOrSpawn(ctx, socketPath, registration, spawn)
}

func Connect(ctx context.Context, socketPath string, registration Registration) (*Client, error) {
	stream, err := dial(ctx, socketPath)
	if err != nil {
		return nil, err
	}
	client := &Client{stream: stream}
	if connection, ok := stream.(interface{ SetDeadline(time.Time) error }); ok {
		_ = connection.SetDeadline(contextDeadline(ctx, 10*time.Second))
		defer connection.SetDeadline(time.Time{})
	}
	if err := client.write(map[string]any{
		"type": "register", "client_type": registration.ClientType, "mode": "stdio",
		"capabilities": registration.Capabilities,
	}); err != nil {
		_ = stream.Close()
		return nil, err
	}
	var response struct {
		Type    string `json:"type"`
		Ready   bool   `json:"ready"`
		Message string `json:"message"`
	}
	if err := readMessage(stream, &response); err != nil {
		_ = stream.Close()
		return nil, err
	}
	if response.Type == "error" {
		_ = stream.Close()
		return nil, errors.New(response.Message)
	}
	if response.Type != "registered" {
		_ = stream.Close()
		return nil, fmt.Errorf("unexpected leader registration response %q", response.Type)
	}
	if !response.Ready {
		if connection, ok := stream.(interface{ SetDeadline(time.Time) error }); ok {
			_ = connection.SetDeadline(contextDeadline(ctx, 5*time.Minute))
		}
		var ready struct {
			Type string `json:"type"`
		}
		if err := readMessage(stream, &ready); err != nil {
			_ = stream.Close()
			return nil, err
		}
		if ready.Type != "leader_ready" {
			_ = stream.Close()
			return nil, fmt.Errorf("unexpected leader readiness response %q", ready.Type)
		}
	}
	return client, nil
}

func contextDeadline(ctx context.Context, maximum time.Duration) time.Time {
	deadline := time.Now().Add(maximum)
	if current, ok := ctx.Deadline(); ok && current.Before(deadline) {
		return current
	}
	return deadline
}

func (c *Client) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = c.stream.Close()
	}()
	writeErr := make(chan error, 1)
	pending := make(map[string]struct{})
	var pendingMu sync.Mutex
	var inputDone atomic.Bool
	go func() {
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 64<<10), maxMessageBytes)
		for scanner.Scan() {
			payload := scanner.Text()
			if payload == "" {
				continue
			}
			id, hasID := acpRequestID(payload)
			if hasID {
				pendingMu.Lock()
				pending[id] = struct{}{}
				pendingMu.Unlock()
			}
			if err := c.write(map[string]any{"type": "acp", "payload": payload}); err != nil {
				if hasID {
					pendingMu.Lock()
					delete(pending, id)
					pendingMu.Unlock()
				}
				writeErr <- err
				return
			}
		}
		err := scanner.Err()
		inputDone.Store(true)
		pendingMu.Lock()
		empty := len(pending) == 0
		pendingMu.Unlock()
		if err == nil && empty {
			err = c.write(map[string]any{"type": "disconnect"})
			_ = c.stream.Close()
		}
		writeErr <- err
	}()
	for {
		var message struct {
			Type    string `json:"type"`
			Payload string `json:"payload"`
			Message string `json:"message"`
		}
		if err := readMessage(c.stream, &message); err != nil {
			pendingMu.Lock()
			empty := len(pending) == 0
			pendingMu.Unlock()
			if inputDone.Load() && empty {
				return nil
			}
			select {
			case writeResult := <-writeErr:
				if writeResult == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
					return writeResult
				}
				return errors.Join(writeResult, err)
			default:
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
		switch message.Type {
		case "acp":
			if _, err := io.WriteString(output, message.Payload+"\n"); err != nil {
				return err
			}
			id, hasID := acpResponseID(message.Payload)
			pendingMu.Lock()
			_, matched := pending[id]
			if hasID && matched {
				delete(pending, id)
			}
			empty := len(pending) == 0
			pendingMu.Unlock()
			if matched && empty && inputDone.Load() {
				_ = c.write(map[string]any{"type": "disconnect"})
				_ = c.stream.Close()
				return nil
			}
		case "error":
			return errors.New(message.Message)
		case "pong":
		default:
			return fmt.Errorf("unexpected leader message %q", message.Type)
		}
	}
}

func acpRequestID(payload string) (string, bool) {
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	ok := json.Unmarshal([]byte(payload), &envelope) == nil &&
		envelope.Method != "" && validRPCID(envelope.ID)
	return string(envelope.ID), ok
}

func acpResponseID(payload string) (string, bool) {
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	ok := json.Unmarshal([]byte(payload), &envelope) == nil &&
		envelope.Method == "" && validRPCID(envelope.ID) &&
		(len(envelope.Result) > 0 || len(envelope.Error) > 0)
	return string(envelope.ID), ok
}

func validRPCID(id json.RawMessage) bool {
	return len(id) > 0 && string(id) != "null"
}

func (c *Client) Close() error {
	_ = c.write(map[string]any{"type": "disconnect"})
	return c.stream.Close()
}

func (c *Client) write(value any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return writeMessage(c.stream, value)
}
