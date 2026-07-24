package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/net/websocket"
)

const agentServerMaxMessageBytes = 8 << 20

type agentWebSocketRelay struct {
	reader *io.PipeReader
	writer *io.PipeWriter

	mu         sync.Mutex
	generation uint64
	connection *websocket.Conn
	buffer     bytes.Buffer
}

func newAgentWebSocketRelay() *agentWebSocketRelay {
	reader, writer := io.Pipe()
	return &agentWebSocketRelay{reader: reader, writer: writer}
}

func (r *agentWebSocketRelay) Read(data []byte) (int, error) {
	return r.reader.Read(data)
}

func (r *agentWebSocketRelay) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = r.buffer.Write(data)
	for {
		line, err := r.buffer.ReadString('\n')
		if err != nil {
			r.buffer.WriteString(line)
			break
		}
		line = strings.TrimSpace(line)
		if line != "" && r.connection != nil {
			_ = websocket.Message.Send(r.connection, line)
		}
	}
	return len(data), nil
}

func (r *agentWebSocketRelay) Close() error {
	r.mu.Lock()
	connection := r.connection
	r.connection = nil
	r.mu.Unlock()
	if connection != nil {
		_ = connection.Close()
	}
	return r.writer.Close()
}

func (r *agentWebSocketRelay) serve(connection *websocket.Conn) {
	connection.MaxPayloadBytes = agentServerMaxMessageBytes

	r.mu.Lock()
	r.generation++
	generation := r.generation
	previous := r.connection
	r.connection = connection
	r.mu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = agentWebSocketPing.Send(connection, nil)
			case <-done:
				return
			}
		}
	}()

	for {
		var message []byte
		if err := websocket.Message.Receive(connection, &message); err != nil {
			break
		}
		trimmed := bytes.TrimSpace(message)
		if len(trimmed) == 0 || !utf8.Valid(trimmed) || bytes.Equal(trimmed, []byte("ping")) {
			continue
		}
		r.mu.Lock()
		current := r.generation == generation
		r.mu.Unlock()
		if !current {
			break
		}
		if _, err := r.writer.Write(append(append([]byte(nil), trimmed...), '\n')); err != nil {
			break
		}
	}

	r.mu.Lock()
	if r.generation == generation {
		r.connection = nil
	}
	r.mu.Unlock()
	_ = connection.Close()
}

func runAgentServer(args []string, options agentServerOptions, stderr io.Writer) error {
	secret := strings.TrimSpace(options.secret)
	if secret == "" {
		var err error
		secret, err = randomAgentServerSecret()
		if err != nil {
			return err
		}
	}
	listener, err := net.Listen("tcp", options.bind)
	if err != nil {
		return fmt.Errorf("listen for agent WebSocket: %w", err)
	}
	relay := newAgentWebSocketRelay()
	defer relay.Close()

	mux := http.NewServeMux()
	mux.Handle("/ws", agentWebSocketHandler(secret, relay))
	httpServer := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	serverErrors := make(chan error, 1)
	serverDone := make(chan error, 1)
	go func() {
		err := httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverErrors <- err
	}()

	fmt.Fprintf(stderr, "[gork] agent server listening on ws://%s/ws\n", listener.Addr())
	fmt.Fprintf(stderr, "[gork] agent server secret: %s\n", secret)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = relay.Close()
		_ = httpServer.Close()
	}()
	go func() {
		err := <-serverErrors
		serverDone <- err
		_ = relay.Close()
	}()

	runErr := runOnce(args, relay, relay, stderr)
	_ = httpServer.Close()
	return errors.Join(runErr, <-serverDone)
}

func agentWebSocketHandler(secret string, relay *agentWebSocketRelay) http.Handler {
	ws := websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler:   relay.serve,
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !agentServerAuthorized(request, secret) {
			http.Error(response, "Invalid or missing authorization token", http.StatusUnauthorized)
			return
		}
		ws.ServeHTTP(response, request)
	})
}

func agentServerAuthorized(request *http.Request, secret string) bool {
	if token, ok := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer "); ok {
		return constantTimeEqual(token, secret)
	}
	return constantTimeEqual(request.URL.Query().Get("server-key"), secret)
}

func constantTimeEqual(value, expected string) bool {
	return len(value) == len(expected) && subtle.ConstantTimeCompare([]byte(value), []byte(expected)) == 1
}

func randomAgentServerSecret() (string, error) {
	data := make([]byte, 9)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate agent server secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

var agentWebSocketPing = websocket.Codec{
	Marshal: func(any) ([]byte, byte, error) { return nil, websocket.PingFrame, nil },
}
