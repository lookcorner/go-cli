package leader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

type ServerConfig struct {
	Root               string
	SocketPath         string
	BinaryVersion      string
	NoExitOnDisconnect bool
}

type Server struct {
	config   ServerConfig
	info     Info
	listener net.Listener
	lock     io.Closer

	inputReader *io.PipeReader
	inputWriter *io.PipeWriter
	inputMu     sync.Mutex
	outputMu    sync.Mutex
	output      bytes.Buffer

	mu            sync.Mutex
	clients       map[uint64]*leaderClient
	requests      map[string]requestRoute
	reverse       map[string]uint64
	sessionOwners map[string]uint64
	hadClient     bool
	ready         chan struct{}
	readyOnce     sync.Once
	nextClient    atomic.Uint64
	nextRequest   atomic.Uint64

	cancel    context.CancelFunc
	closeOnce sync.Once
	done      chan error
}

type leaderClient struct {
	stream       io.ReadWriteCloser
	clientType   string
	capabilities ClientCapabilities
	mu           sync.Mutex
}

type ClientCapabilities struct {
	YoloMode     bool   `json:"yolo_mode"`
	AutoMode     bool   `json:"auto_mode"`
	DefaultModel string `json:"default_model"`
	CodeNav      bool   `json:"code_nav_enabled"`
	Terminal     bool   `json:"terminal"`
	FSRead       bool   `json:"fs_read"`
	FSWrite      bool   `json:"fs_write"`
}

type requestRoute struct {
	clientID uint64
	id       json.RawMessage
	method   string
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func Start(ctx context.Context, config ServerConfig) (*Server, error) {
	if config.Root == "" {
		return nil, errors.New("leader root is required")
	}
	ctx, cancel := context.WithCancel(ctx)
	server := &Server{
		config:        config,
		clients:       make(map[uint64]*leaderClient),
		requests:      make(map[string]requestRoute),
		reverse:       make(map[string]uint64),
		sessionOwners: make(map[string]uint64),
		ready:         make(chan struct{}),
		cancel:        cancel,
		done:          make(chan error, 1),
	}
	server.inputReader, server.inputWriter = io.Pipe()
	if err := server.startPlatform(); err != nil {
		cancel()
		_ = server.inputReader.Close()
		_ = server.inputWriter.Close()
		return nil, err
	}
	go server.serve(ctx)
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	return server, nil
}

func (s *Server) Read(data []byte) (int, error) {
	return s.inputReader.Read(data)
}

func (s *Server) Write(data []byte) (int, error) {
	s.outputMu.Lock()
	defer s.outputMu.Unlock()
	_, _ = s.output.Write(data)
	for {
		line, err := s.output.ReadString('\n')
		if err != nil {
			s.output.WriteString(line)
			break
		}
		line = string(bytes.TrimSpace([]byte(line)))
		if line != "" {
			s.routeOutput([]byte(line))
		}
	}
	return len(data), nil
}

func (s *Server) MarkReady() {
	s.readyOnce.Do(func() { close(s.ready) })
}

func (s *Server) SocketPath() string {
	return s.info.SocketPath
}

func (s *Server) Wait() error {
	return <-s.done
}

func (s *Server) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		s.cancel()
		if s.listener != nil {
			closeErr = errors.Join(closeErr, s.listener.Close())
		}
		s.mu.Lock()
		clients := make([]*leaderClient, 0, len(s.clients))
		for _, client := range s.clients {
			clients = append(clients, client)
		}
		s.clients = make(map[uint64]*leaderClient)
		s.mu.Unlock()
		for _, client := range clients {
			closeErr = errors.Join(closeErr, client.stream.Close())
		}
		closeErr = errors.Join(closeErr, s.inputWriter.Close())
		closeErr = errors.Join(closeErr, s.cleanupPlatform())
	})
	return closeErr
}

func (s *Server) serve(ctx context.Context) {
	var serveErr error
	defer func() {
		_ = s.Close()
		s.done <- serveErr
		close(s.done)
	}()
	for {
		stream, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				serveErr = err
			}
			return
		}
		go s.serveClient(ctx, stream)
	}
}

func (s *Server) serveClient(ctx context.Context, stream io.ReadWriteCloser) {
	var register struct {
		Type         string             `json:"type"`
		ClientType   string             `json:"client_type"`
		Mode         string             `json:"mode"`
		Capabilities ClientCapabilities `json:"capabilities"`
	}
	if err := readMessage(stream, &register); err != nil || register.Type != "register" {
		_ = writeMessage(stream, map[string]any{"type": "error", "code": -32600, "message": "registration required"})
		_ = stream.Close()
		return
	}
	if register.Mode != "stdio" {
		_ = writeMessage(stream, map[string]any{"type": "error", "code": -32601, "message": "headless relay mode is not supported"})
		_ = stream.Close()
		return
	}
	clientID := s.nextClient.Add(1)
	client := &leaderClient{stream: stream, clientType: register.ClientType, capabilities: register.Capabilities}
	s.mu.Lock()
	s.clients[clientID] = client
	s.hadClient = true
	s.mu.Unlock()
	defer s.removeClient(clientID)

	ready := false
	select {
	case <-s.ready:
		ready = true
	default:
	}
	if err := client.write(map[string]any{
		"type": "registered", "client_id": clientID, "ready": ready,
		"leader_protocol_version": ProtocolVersion,
		"leader_binary_version":   s.config.BinaryVersion,
		"leader_capabilities": map[string]any{
			"control_v1": true, "runtime_cpu_profile": false,
			"profile_formats": []any{}, "workspace_exposure": false, "relaunch_v1": false,
		},
	}); err != nil {
		return
	}
	if !ready {
		select {
		case <-ctx.Done():
			return
		case <-s.ready:
			if err := client.write(map[string]any{"type": "leader_ready"}); err != nil {
				return
			}
		}
	}
	for {
		var message struct {
			Type      string          `json:"type"`
			Payload   string          `json:"payload"`
			RequestID string          `json:"request_id"`
			Command   json.RawMessage `json:"command"`
		}
		if err := readMessage(stream, &message); err != nil {
			return
		}
		switch message.Type {
		case "acp":
			if err := s.routeInput(clientID, client, []byte(message.Payload)); err != nil {
				_ = client.write(map[string]any{"type": "error", "code": -32600, "message": err.Error()})
			}
		case "control":
			s.handleControl(client, message.RequestID, message.Command)
		case "ping":
			_ = client.write(map[string]any{"type": "pong"})
		case "disconnect":
			return
		default:
			_ = client.write(map[string]any{"type": "error", "code": -32601, "message": "unsupported leader message"})
		}
	}
}

func (s *Server) handleControl(client *leaderClient, requestID string, command json.RawMessage) {
	var value struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(command, &value) != nil || value.Type != "get_leader_info" {
		_ = client.write(map[string]any{
			"type": "control_result", "request_id": requestID,
			"result": map[string]any{"Err": map[string]any{"code": "unsupported", "message": "unsupported control command"}},
		})
		return
	}
	info := map[string]any{
		"type": "leader_info", "pid": s.info.PID, "socket_path": s.info.SocketPath,
		"lock_path": s.info.LockPath, "ws_url_suffix": s.info.WSURLSuffix,
		"leader_protocol_version": s.info.LeaderProtocolVersion,
		"leader_binary_version":   s.info.LeaderBinaryVersion,
		"profiling_supported":     false, "profiling_compiled_in": false,
		"cpu_profile_active": false, "cpu_profile_stopping": false,
		"profile_started_at": nil, "profile_formats": []any{},
	}
	_ = client.write(map[string]any{
		"type": "control_result", "request_id": requestID, "result": map[string]any{"Ok": info},
	})
}

func (s *Server) routeInput(clientID uint64, client *leaderClient, payload []byte) error {
	var message rpcEnvelope
	if err := json.Unmarshal(payload, &message); err != nil {
		return fmt.Errorf("invalid ACP payload: %w", err)
	}
	if message.Method == "initialize" {
		injectClientIdentity(&message, client.clientType)
		payload, _ = json.Marshal(message)
	}
	if message.Method == "session/new" || message.Method == "session/load" {
		injectClientCapabilities(&message, clientID, client.clientType, client.capabilities)
		payload, _ = json.Marshal(message)
	}
	if message.Method == "session/set_model" {
		var params struct {
			ModelID string `json:"modelId"`
		}
		if json.Unmarshal(message.Params, &params) == nil && params.ModelID != "" {
			client.capabilities.DefaultModel = params.ModelID
		}
	}
	if sessionID := payloadSessionID(message.Params); sessionID != "" {
		s.mu.Lock()
		owner := s.sessionOwners[sessionID]
		_, ownerConnected := s.clients[owner]
		s.mu.Unlock()
		reconnect := message.Method == "session/load" || message.Method == "session/resume"
		if owner != 0 && owner != clientID && (ownerConnected || !reconnect) {
			return errors.New("ACP session belongs to another leader client")
		}
	}
	if len(message.ID) > 0 {
		if message.Method == "" {
			key := string(message.ID)
			s.mu.Lock()
			target, ok := s.reverse[key]
			if ok && target == clientID {
				delete(s.reverse, key)
			}
			s.mu.Unlock()
			if !ok || target != clientID {
				return errors.New("ACP response does not belong to this client")
			}
		} else {
			internalID := "leader-" + strconv.FormatUint(s.nextRequest.Add(1), 10)
			encoded, _ := json.Marshal(internalID)
			s.mu.Lock()
			s.requests[string(encoded)] = requestRoute{clientID: clientID, id: append(json.RawMessage(nil), message.ID...), method: message.Method}
			s.mu.Unlock()
			message.ID = encoded
			payload, _ = json.Marshal(message)
		}
	}
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	_, err := s.inputWriter.Write(append(append([]byte(nil), payload...), '\n'))
	return err
}

func injectClientIdentity(message *rpcEnvelope, clientType string) {
	if clientType == "" {
		return
	}
	var params map[string]any
	if json.Unmarshal(message.Params, &params) != nil {
		params = make(map[string]any)
	}
	meta, _ := params["_meta"].(map[string]any)
	if meta == nil {
		meta = make(map[string]any)
	}
	if _, exists := meta["clientIdentifier"]; !exists {
		meta["clientIdentifier"] = clientType
	}
	params["_meta"] = meta
	message.Params, _ = json.Marshal(params)
}

func injectClientCapabilities(message *rpcEnvelope, clientID uint64, clientType string, capabilities ClientCapabilities) {
	var params map[string]any
	if json.Unmarshal(message.Params, &params) != nil {
		params = make(map[string]any)
	}
	meta, _ := params["_meta"].(map[string]any)
	if meta == nil {
		meta = make(map[string]any)
	}
	if message.Method == "session/new" && capabilities.YoloMode {
		if _, exists := meta["yoloMode"]; !exists {
			meta["yoloMode"] = true
		}
	}
	if capabilities.AutoMode && !capabilities.YoloMode {
		if _, exists := meta["autoMode"]; !exists {
			meta["autoMode"] = true
		}
	}
	if message.Method == "session/new" && capabilities.DefaultModel != "" {
		if _, exists := meta["modelId"]; !exists {
			meta["modelId"] = capabilities.DefaultModel
		}
	}
	if clientType != "" {
		if _, exists := meta["clientIdentifier"]; !exists {
			meta["clientIdentifier"] = clientType
		}
	}
	if _, exists := meta["x.ai/leaderClientId"]; !exists {
		meta["x.ai/leaderClientId"] = clientID
	}
	meta["codeNavEnabled"] = capabilities.CodeNav
	meta["clientTerminal"] = capabilities.Terminal
	meta["clientFsRead"] = capabilities.FSRead
	meta["clientFsWrite"] = capabilities.FSWrite
	params["_meta"] = meta
	message.Params, _ = json.Marshal(params)
}

func (s *Server) routeOutput(payload []byte) {
	var message rpcEnvelope
	if json.Unmarshal(payload, &message) != nil {
		s.broadcastACP(payload)
		return
	}
	if message.Method == "" && len(message.ID) > 0 {
		key := string(message.ID)
		s.mu.Lock()
		route, ok := s.requests[key]
		if ok {
			delete(s.requests, key)
			if sessionID := responseSessionID(message.Result); sessionID != "" {
				s.sessionOwners[sessionID] = route.clientID
			}
		}
		s.mu.Unlock()
		if ok {
			message.ID = route.id
			restored, _ := json.Marshal(message)
			s.sendACP(route.clientID, restored)
		}
		return
	}
	sessionID := payloadSessionID(message.Params)
	if message.Method != "" && len(message.ID) > 0 {
		target := s.ownerOrFirst(sessionID)
		if target == 0 {
			return
		}
		s.mu.Lock()
		s.reverse[string(message.ID)] = target
		s.mu.Unlock()
		s.sendACP(target, payload)
		return
	}
	if sessionID != "" {
		if owner := s.ownerOrFirst(sessionID); owner != 0 {
			s.sendACP(owner, payload)
		}
		return
	}
	s.broadcastACP(payload)
}

func responseSessionID(result json.RawMessage) string {
	var value struct {
		SessionID      string `json:"sessionId"`
		SessionIDSnake string `json:"session_id"`
	}
	_ = json.Unmarshal(result, &value)
	if value.SessionID == "" {
		return value.SessionIDSnake
	}
	return value.SessionID
}

func payloadSessionID(params json.RawMessage) string {
	var value struct {
		SessionID      string `json:"sessionId"`
		SessionIDSnake string `json:"session_id"`
	}
	_ = json.Unmarshal(params, &value)
	if value.SessionID != "" {
		return value.SessionID
	}
	return value.SessionIDSnake
}

func (s *Server) ownerOrFirst(sessionID string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sessionID != "" {
		owner, known := s.sessionOwners[sessionID]
		if known {
			if _, ok := s.clients[owner]; ok {
				return owner
			}
			return 0
		}
		return 0
	}
	var first uint64
	for id := range s.clients {
		if first == 0 || id < first {
			first = id
		}
	}
	return first
}

func (s *Server) sendACP(clientID uint64, payload []byte) {
	s.mu.Lock()
	client := s.clients[clientID]
	s.mu.Unlock()
	if client != nil {
		_ = client.write(map[string]any{"type": "acp", "payload": string(payload)})
	}
}

func (s *Server) broadcastACP(payload []byte) {
	s.mu.Lock()
	clients := make([]*leaderClient, 0, len(s.clients))
	for _, client := range s.clients {
		clients = append(clients, client)
	}
	s.mu.Unlock()
	for _, client := range clients {
		_ = client.write(map[string]any{"type": "acp", "payload": string(payload)})
	}
}

func (s *Server) removeClient(clientID uint64) {
	s.mu.Lock()
	client := s.clients[clientID]
	delete(s.clients, clientID)
	for key, route := range s.requests {
		if route.clientID == clientID {
			delete(s.requests, key)
		}
	}
	for key, owner := range s.reverse {
		if owner == clientID {
			delete(s.reverse, key)
		}
	}
	shouldClose := s.hadClient && len(s.clients) == 0 && !s.config.NoExitOnDisconnect
	s.mu.Unlock()
	if client != nil {
		_ = client.stream.Close()
	}
	if shouldClose {
		_ = s.Close()
	}
}

func (c *leaderClient) write(value any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return writeMessage(c.stream, value)
}

func (s *Server) paths() (socketPath, lockPath string) {
	socketPath = s.config.SocketPath
	if socketPath == "" {
		socketPath = os.Getenv("GROK_LEADER_SOCKET")
	}
	if socketPath == "" {
		socketPath = filepath.Join(s.config.Root, "leader.sock")
	}
	return socketPath, strings.TrimSuffix(socketPath, filepath.Ext(socketPath)) + ".lock"
}
