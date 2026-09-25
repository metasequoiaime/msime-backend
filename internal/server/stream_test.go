package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/metasequoiaime/MSIME-Backend/internal/contract"
)

func streamFixture(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Api-Key") != "provider-secret" || r.Header.Get("X-Api-Resource-Id") != "synthetic-resource" || r.Header.Get("X-Api-Request-Id") == "" {
			t.Error("stream credential isolation failed")
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(contract.StreamMessageBytes)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for {
			kind, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if c.Write(ctx, kind, data) != nil {
				return
			}
		}
	})
	s.config.Streaming = StreamingEndpoint{URL: strings.Replace(s.config.Cloud.URL, "https:", "wss:", 1), token: "provider-secret", ResourceID: "synthetic-resource", MaxSeconds: 5}
	live := httptest.NewTLSServer(s)
	t.Cleanup(func() { s.Close(); live.Close() })
	return s, live
}
func dialStream(t *testing.T, live *httptest.Server) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, strings.Replace(live.URL, "https:", "wss:", 1)+contract.StreamingTranscriptionPath, &websocket.DialOptions{HTTPClient: live.Client(), HTTPHeader: http.Header{"Authorization": []string{"Bearer " + testToken}, "X-Api-Key": []string{"untrusted-client-provider-key"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	return c
}
func TestStreamBinaryRelayAndShutdown(t *testing.T) {
	s, live := streamFixture(t)
	c := dialStream(t, live)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// 包含内嵌 NUL 和负序列号字节，转发时必须保留二进制消息边界。
	for _, packet := range [][]byte{{0x11, 0x21, 0x01, 0, 0, 0, 0, 1}, {0x11, 0x23, 0x01, 0, 255, 255, 255, 254}} {
		if err := c.Write(ctx, websocket.MessageBinary, packet); err != nil {
			t.Fatal(err)
		}
		kind, data, err := c.Read(ctx)
		if err != nil || kind != websocket.MessageBinary || !bytes.Equal(data, packet) {
			t.Fatalf("binary relay failed: %v", err)
		}
	}
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown left stream running")
	}
	if _, _, err := c.Read(ctx); err == nil {
		t.Fatal("stream remained open")
	}
	// Close waits for the stream handler, but the slot is released by the request middleware around it, just after the handler returns. A leak is a slot that never comes back, so wait for it rather than read it the instant Close returns.
	released := time.Now().Add(2 * time.Second)
	for len(s.slots) != 0 && time.Now().Before(released) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(s.slots) != 0 {
		t.Fatal("stream leaked concurrency slot")
	}
}
func TestStreamRejectsTextAndOversizedMessage(t *testing.T) {
	for _, size := range []int{1, contract.StreamMessageBytes + 1} {
		t.Run(string(rune(size%26+'a')), func(t *testing.T) {
			_, live := streamFixture(t)
			c := dialStream(t, live)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			kind := websocket.MessageBinary
			if size == 1 {
				kind = websocket.MessageText
			}
			_ = c.Write(ctx, kind, make([]byte, size))
			if _, _, err := c.Read(ctx); err == nil {
				t.Fatal("invalid message accepted")
			}
		})
	}
}
func TestStreamAuthenticationAndExpiry(t *testing.T) {
	s, live := streamFixture(t)
	response := call(s, "GET", contract.StreamingTranscriptionPath, "")
	if response.Code != 400 {
		t.Fatal(response.Code)
	}
	req, _ := http.NewRequest("GET", live.URL+contract.StreamingTranscriptionPath, nil)
	res, err := live.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal(res.StatusCode)
	}
	s.config.Streaming.MaxSeconds = 1
	c := dialStream(t, live)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, _, err := c.Read(ctx); err == nil {
		t.Fatal("session did not expire")
	}
}

func TestStreamConfig(t *testing.T) {
	t.Setenv("STREAM_CLIENT", testToken)
	t.Setenv("STREAM_PROVIDER", "synthetic-provider-key")
	t.Setenv("STREAM_APP", "synthetic-app-key")
	base := func() Config {
		return Config{Clients: []Client{{ID: "test", TokenEnv: "STREAM_CLIENT", RequestsPerMinute: 10}}, Streaming: StreamingEndpoint{URL: "wss://example.com/asr", TokenEnv: "STREAM_PROVIDER", AppKeyEnv: "STREAM_APP", ResourceID: "resource"}}
	}
	c := base()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Streaming.MaxSeconds != 120 || c.Streaming.appKey != "synthetic-app-key" {
		t.Fatal("stream defaults not loaded")
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.Streaming.URL = "ws://example.com/asr" },
		func(c *Config) { c.Streaming.URL = "wss://user:password@example.com/asr" },
		func(c *Config) { c.Streaming.URL = "wss://example.com/asr?token=x" },
		func(c *Config) { c.Streaming.TokenEnv = "MISSING_STREAM_KEY" },
		func(c *Config) { c.Streaming.AppKeyEnv = "MISSING_STREAM_APP" },
		func(c *Config) { c.Streaming.ResourceID = "bad\r\nheader" },
		func(c *Config) { c.Streaming.MaxSeconds = 601 },
	} {
		c := base()
		change(&c)
		if c.Validate() == nil {
			t.Fatal("invalid stream config accepted")
		}
	}
}

func TestEveryAPIStreamUsesServerBearerAndModel(t *testing.T) {
	s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer partner-secret" || r.URL.Query().Get("model") != "volc.seedasr.sauc.duration" || r.Header.Get("X-Api-Key") != "" || r.Header.Get("X-Api-Resource-Id") != "" {
			t.Error("合作接口鉴权或模型隔离失败")
		}
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer c.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		kind, data, e := c.Read(ctx)
		if e != nil {
			return
		}
		c.Write(ctx, kind, data)
	})
	s.config.Streaming = StreamingEndpoint{Provider: "everyapi", Model: "volc.seedasr.sauc.duration", URL: strings.Replace(s.config.Cloud.URL, "https:", "wss:", 1), token: "partner-secret", MaxSeconds: 5}
	live := httptest.NewTLSServer(s)
	defer live.Close()
	defer s.Close()
	c := dialStream(t, live)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	packet := []byte{0x11, 0x23, 0x01, 0, 0, 0, 0, 1}
	if e := c.Write(ctx, websocket.MessageBinary, packet); e != nil {
		t.Fatal(e)
	}
	_, data, e := c.Read(ctx)
	if e != nil || !bytes.Equal(data, packet) {
		t.Fatal("二进制转发失败", e)
	}
}
