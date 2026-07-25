package voice

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestSTTURL(t *testing.T) {
	for _, base := range []string{"https://proxy.example/xai/v1/", "proxy.example/xai/v1/"} {
		t.Run(base, func(t *testing.T) {
			target, err := sttURL(Config{
				BaseURL: base, Language: "ja", SampleRate: 16_000, EndpointingMS: 500,
			})
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := url.Parse(target)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Scheme != "wss" || parsed.Host != "proxy.example" || parsed.Path != "/xai/v1/stt" {
				t.Fatalf("unexpected STT URL: %s", target)
			}
			query := parsed.Query()
			for key, want := range map[string]string{
				"sample_rate": "16000", "encoding": "pcm", "interim_results": "true", "language": "ja", "endpointing": "500",
			} {
				if got := query.Get(key); got != want {
					t.Fatalf("%s = %q, want %q", key, got, want)
				}
			}
		})
	}
}

type testRecorder struct{}

func (testRecorder) Start(_ context.Context, _ int, output chan<- []byte) (capture, error) {
	output <- []byte{1, 2, 3, 4}
	return testCapture{}, nil
}

type testCapture struct{}

func (testCapture) Stop() {}

func TestClientStreamsAudioAndTranscripts(t *testing.T) {
	requireLoopback(t)
	server := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		_ = websocket.Message.Send(conn, `{"type":"transcript.created"}`)
		var audio []byte
		if err := websocket.Message.Receive(conn, &audio); err != nil {
			return
		}
		if string(audio) != string([]byte{1, 2, 3, 4}) {
			t.Errorf("audio = %v", audio)
			return
		}
		_ = websocket.Message.Send(conn, `{"type":"transcript.partial","text":"hello","is_final":false,"speech_final":false}`)
		var done string
		if err := websocket.Message.Receive(conn, &done); err != nil || !strings.Contains(done, "audio.done") {
			t.Errorf("done = %q, err = %v", done, err)
			return
		}
		_ = websocket.Message.Send(conn, `{"type":"transcript.done","text":"hello world"}`)
	}))
	defer server.Close()

	client := New(Config{BaseURL: "https://api.x.ai/v1"}, "", func(context.Context, string) (string, error) {
		return "fresh-token", nil
	})
	client.recorder = testRecorder{}
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	client.dial = func(context.Context, *websocket.Config) (*websocket.Conn, error) {
		return websocket.Dial(wsURL, "", "http://localhost")
	}
	session, err := client.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	event := receiveVoiceEvent(t, session.Events())
	if event.Text != "hello" || event.Final || event.Err != nil {
		t.Fatalf("partial event = %#v", event)
	}
	session.Stop()
	event = receiveVoiceEvent(t, session.Events())
	if event.Text != "hello world" || !event.Final || event.Err != nil {
		t.Fatalf("final event = %#v", event)
	}
	select {
	case _, ok := <-session.Events():
		if ok {
			t.Fatal("voice event stream did not close")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for voice session close")
	}
}

func receiveVoiceEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for voice event")
		return Event{}
	}
}

func TestSTTURLRequiresTLS(t *testing.T) {
	if _, err := sttURL(Config{BaseURL: "http://localhost:8080"}); err == nil {
		t.Fatal("expected plaintext endpoint to be rejected")
	}
}

func TestNewAppliesVoiceDefaults(t *testing.T) {
	client := New(Config{}, "", nil)
	if client.config.SampleRate != 16_000 || client.config.EndpointingMS != 400 || client.config.Language != "en" {
		t.Fatalf("unexpected defaults: %#v", client.config)
	}
}

func TestVoiceLanguageCanonicalizationAndLocaleResolution(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "", want: "en"},
		{value: "ES", want: "es"},
		{value: "pt_BR.UTF-8", want: "pt"},
		{value: "tl-PH", want: "fil"},
		{value: "zh-CN", want: "en"},
		{value: "auto", want: "auto"},
	} {
		if got := CanonicalLanguage(test.value); got != test.want {
			t.Errorf("CanonicalLanguage(%q)=%q want %q", test.value, got, test.want)
		}
	}

	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "tl_PH.UTF-8")
	t.Setenv("LANG", "es_ES.UTF-8")
	if got := LanguageForAPI("auto"); got != "fil" {
		t.Fatalf("auto language=%q", got)
	}
	t.Setenv("LC_ALL", "C")
	if got := LanguageForAPI("auto"); got != "en" {
		t.Fatalf("C locale language=%q", got)
	}
}

func TestClientLanguageUpdateAppliesToNextConnection(t *testing.T) {
	client := New(Config{Language: "en"}, "token", nil)
	client.SetLanguage("ES")
	var got string
	client.dial = func(_ context.Context, config *websocket.Config) (*websocket.Conn, error) {
		got = config.Location.Query().Get("language")
		return nil, errors.New("stop after inspecting handshake")
	}
	if _, err := client.Start(context.Background()); err == nil {
		t.Fatal("voice start unexpectedly succeeded")
	}
	if got != "es" {
		t.Fatalf("language=%q", got)
	}
}
