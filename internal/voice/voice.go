package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

const (
	defaultSampleRate = 16_000
	connectTimeout    = 15 * time.Second
	readyTimeout      = 10 * time.Second
)

type TokenProvider func(context.Context, string) (string, error)

type Config struct {
	BaseURL       string
	Language      string
	SampleRate    int
	EndpointingMS int
}

type Event struct {
	Text  string
	Final bool
	Err   error
}

type Session interface {
	Events() <-chan Event
	Stop()
}

type Client struct {
	config   Config
	token    string
	provider TokenProvider
	recorder recorder
	dial     func(context.Context, *websocket.Config) (*websocket.Conn, error)
}

func New(config Config, token string, provider TokenProvider) *Client {
	if config.SampleRate <= 0 {
		config.SampleRate = defaultSampleRate
	}
	if config.EndpointingMS <= 0 {
		config.EndpointingMS = 400
	}
	if strings.TrimSpace(config.Language) == "" {
		config.Language = "en"
	}
	return &Client{
		config: config, token: strings.TrimSpace(token), provider: provider,
		recorder: platformRecorder{}, dial: func(ctx context.Context, config *websocket.Config) (*websocket.Conn, error) {
			return config.DialContext(ctx)
		},
	}
}

func (c *Client) Start(ctx context.Context) (Session, error) {
	token := c.token
	if c.provider != nil {
		var err error
		token, err = c.provider(ctx, "")
		if err != nil {
			return nil, fmt.Errorf("voice authentication: %w", err)
		}
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("voice requires login or an API key")
	}
	target, err := sttURL(c.config)
	if err != nil {
		return nil, err
	}
	wsConfig, err := websocket.NewConfig(target, "https://gork.local")
	if err != nil {
		return nil, fmt.Errorf("voice websocket config: %w", err)
	}
	wsConfig.Header = http.Header{
		"Authorization":            {"Bearer " + token},
		"User-Agent":               {"gork-go/0.1"},
		"X-Grok-Client-Identifier": {"gork-go"},
	}
	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	conn, err := c.dial(connectCtx, wsConfig)
	if err != nil {
		return nil, fmt.Errorf("connect voice STT: %w", err)
	}
	if err := waitReady(conn); err != nil {
		conn.Close()
		return nil, err
	}
	sessionCtx, sessionCancel := context.WithCancel(ctx)
	s := &activeSession{
		conn: conn, events: make(chan Event, 32), audio: make(chan []byte, 64),
		stop: make(chan struct{}), cancel: sessionCancel,
	}
	capture, err := c.recorder.Start(sessionCtx, c.config.SampleRate, s.audio)
	if err != nil {
		sessionCancel()
		conn.Close()
		return nil, fmt.Errorf("start microphone: %w", err)
	}
	s.capture = capture
	go s.write()
	go s.read(sessionCtx)
	return s, nil
}

func sttURL(config Config) (string, error) {
	base := strings.TrimSpace(config.BaseURL)
	if base == "" {
		base = "https://api.x.ai/v1"
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse voice base URL: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		parsed.Scheme = "wss"
	case "wss":
	case "http", "ws":
		return "", errors.New("voice requires a TLS API endpoint")
	default:
		return "", fmt.Errorf("voice API URL has unsupported scheme %q", parsed.Scheme)
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(path, "/v1") {
		path += "/v1"
	}
	parsed.Path = path + "/stt"
	query := parsed.Query()
	query.Set("sample_rate", fmt.Sprint(config.SampleRate))
	query.Set("encoding", "pcm")
	query.Set("interim_results", "true")
	query.Set("language", config.Language)
	query.Set("endpointing", fmt.Sprint(config.EndpointingMS))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func waitReady(conn *websocket.Conn) error {
	if err := conn.SetReadDeadline(time.Now().Add(readyTimeout)); err != nil {
		return err
	}
	defer conn.SetReadDeadline(time.Time{})
	var raw string
	if err := websocket.Message.Receive(conn, &raw); err != nil {
		return fmt.Errorf("wait for voice STT: %w", err)
	}
	var event serverEvent
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		return fmt.Errorf("decode voice STT ready event: %w", err)
	}
	if event.Type == "error" {
		return errors.New(event.Message)
	}
	if event.Type != "transcript.created" {
		return fmt.Errorf("unexpected voice STT event %q", event.Type)
	}
	return nil
}

type serverEvent struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	IsFinal     bool   `json:"is_final"`
	SpeechFinal bool   `json:"speech_final"`
	Message     string `json:"message"`
}

type recorder interface {
	Start(context.Context, int, chan<- []byte) (capture, error)
}

type capture interface {
	Stop()
}

type activeSession struct {
	conn    *websocket.Conn
	capture capture
	events  chan Event
	audio   chan []byte
	stop    chan struct{}
	cancel  context.CancelFunc
	once    sync.Once
}

func (s *activeSession) Events() <-chan Event { return s.events }

func (s *activeSession) Stop() {
	s.once.Do(func() {
		s.capture.Stop()
		close(s.stop)
		time.AfterFunc(readyTimeout, func() {
			s.cancel()
			s.conn.Close()
		})
	})
}

func (s *activeSession) write() {
	for {
		select {
		case chunk := <-s.audio:
			if len(chunk) > 0 {
				if err := websocket.Message.Send(s.conn, chunk); err != nil {
					s.cancel()
					return
				}
			}
		case <-s.stop:
			_ = websocket.Message.Send(s.conn, `{"type":"audio.done"}`)
			return
		}
	}
}

func (s *activeSession) read(ctx context.Context) {
	defer func() {
		s.Stop()
		s.cancel()
		s.conn.Close()
		close(s.events)
	}()
	rawEvents := make(chan serverEvent, 32)
	readErrors := make(chan error, 1)
	go func() {
		defer close(rawEvents)
		for {
			var raw string
			if err := websocket.Message.Receive(s.conn, &raw); err != nil {
				readErrors <- err
				return
			}
			var event serverEvent
			if err := json.Unmarshal([]byte(raw), &event); err != nil {
				readErrors <- fmt.Errorf("decode voice STT event: %w", err)
				return
			}
			select {
			case rawEvents <- event:
			case <-ctx.Done():
				return
			}
		}
	}()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	heard := false
	locked := ""
	lastFinal := ""
	stop := s.stop
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			stop = nil
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			if !heard {
				s.emit(Event{Err: errors.New("no audio detected in 10s; check microphone permission")})
				return
			}
		case err := <-readErrors:
			if ctx.Err() == nil && err != nil && !benignDisconnect(err) {
				s.emit(Event{Err: fmt.Errorf("voice connection lost: %w", err)})
			}
			return
		case event, ok := <-rawEvents:
			if !ok {
				return
			}
			text := strings.TrimSpace(event.Text)
			switch event.Type {
			case "transcript.partial":
				if text == "" {
					continue
				}
				heard = true
				if event.SpeechFinal {
					locked = ""
					lastFinal = text
					s.emit(Event{Text: text, Final: true})
				} else if event.IsFinal {
					locked = strings.TrimSpace(locked + " " + text)
					s.emit(Event{Text: locked})
				} else {
					s.emit(Event{Text: strings.TrimSpace(locked + " " + text)})
				}
			case "transcript.done":
				if text != "" && text != lastFinal {
					heard = true
					locked = ""
					s.emit(Event{Text: text, Final: true})
				}
				return
			case "error":
				s.emit(Event{Err: errors.New(event.Message)})
				return
			}
		}
	}
}

func (s *activeSession) emit(event Event) {
	select {
	case s.events <- event:
	default:
	}
}

func benignDisconnect(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "closed network connection")
}
