package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// 仅显式启用时访问合作服务，音频必须为不超过十秒的合成 PCM。
func TestPartnerStreamingLive(t *testing.T) {
	key, path := os.Getenv("MSIME_LIVE_STREAM_TOKEN"), os.Getenv("MSIME_LIVE_STREAM_PCM")
	if key == "" || path == "" {
		t.Skip("设置 MSIME_LIVE_STREAM_TOKEN / MSIME_LIVE_STREAM_PCM 执行合成音频实测")
	}
	pcm, e := os.ReadFile(path)
	if e != nil || len(pcm) == 0 || len(pcm) > 320000 || len(pcm)%2 != 0 {
		t.Fatal("需要十秒以内的 16kHz 单声道 s16le PCM")
	}
	t.Setenv("LIVE_DEVICE_TOKEN", strings.Repeat("d", 64))
	s, e := New(Config{Clients: []Client{{ID: "live-test", TokenEnv: "LIVE_DEVICE_TOKEN", RequestsPerMinute: 120}}, Streaming: StreamingEndpoint{Provider: "everyapi", URL: "wss://api.everyapi.ai/v1/audio/stream", TokenEnv: "MSIME_LIVE_STREAM_TOKEN", Model: "volc.seedasr.sauc.duration", MaxSeconds: 30}})
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(s)
	defer server.Close()
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	conn, res, e := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/audio/stream", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + strings.Repeat("d", 64)}}})
	if e != nil {
		if res != nil {
			t.Fatalf("实时握手失败，HTTP %d", res.StatusCode)
		}
		t.Fatal("实时握手失败")
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	packet := func(kind, flags byte, sequence int32, payload []byte) []byte {
		var z bytes.Buffer
		w := gzip.NewWriter(&z)
		w.Write(payload)
		w.Close()
		b := []byte{0x11, kind<<4 | flags, 0x01, 0}
		if kind == 1 {
			b[2] = 0x11
		}
		b = binary.BigEndian.AppendUint32(b, uint32(sequence))
		b = binary.BigEndian.AppendUint32(b, uint32(z.Len()))
		return append(b, z.Bytes()...)
	}
	config := []byte(`{"user":{"uid":"msime-synthetic-test"},"audio":{"format":"pcm","rate":16000,"bits":16,"channel":1},"request":{"model_name":"bigmodel","enable_itn":true,"enable_punc":true}}`)
	if e = conn.Write(ctx, websocket.MessageBinary, packet(1, 1, 1, config)); e != nil {
		t.Fatal("发送音频配置失败")
	}
	var finalSent atomic.Bool
	sent := make(chan error, 1)
	go func() {
		seq := int32(2)
		for off := 0; off < len(pcm); off += 6400 {
			end := min(off+6400, len(pcm))
			flags := byte(1)
			n := seq
			if end == len(pcm) {
				flags = 3
				n = -seq
				finalSent.Store(true)
			}
			if e := conn.Write(ctx, websocket.MessageBinary, packet(2, flags, n, pcm[off:end])); e != nil {
				sent <- e
				return
			}
			seq++
			select {
			case <-ctx.Done():
				sent <- ctx.Err()
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
		sent <- nil
	}()
	defer func() { cancel(); <-sent }()
	partial, finalText := false, ""
	for {
		_, b, e := conn.Read(ctx)
		if e != nil {
			t.Fatalf("读取实时结果失败: status=%d error=%v", websocket.CloseStatus(e), e)
		}
		if len(b) < 8 {
			t.Fatal("响应帧过短")
		}
		flags := b[1] & 15
		kind := b[1] >> 4
		offset := int(b[0]&15) * 4
		if kind == 15 {
			t.Fatal("合作接口返回语音错误")
		}
		if flags&1 != 0 {
			offset += 4
		}
		if len(b) < offset+4 {
			t.Fatal("响应帧缺少长度")
		}
		size := int(binary.BigEndian.Uint32(b[offset : offset+4]))
		offset += 4
		if size != len(b)-offset {
			t.Fatal("响应长度不一致")
		}
		payload := b[offset:]
		if b[2]&15 == 1 {
			r, e := gzip.NewReader(bytes.NewReader(payload))
			if e != nil {
				t.Fatal(e)
			}
			payload, e = io.ReadAll(io.LimitReader(r, 1<<20))
			r.Close()
			if e != nil {
				t.Fatal(e)
			}
		}
		var v struct {
			Result struct {
				Text string `json:"text"`
			} `json:"result"`
		}
		if json.Unmarshal(payload, &v) != nil {
			t.Fatal("响应 JSON 无效")
		}
		if v.Result.Text != "" && !finalSent.Load() {
			partial = true
		}
		if flags&2 != 0 {
			finalText = v.Result.Text
			break
		}
	}
	if finalText == "" || !partial {
		t.Fatalf("实时识别不完整：partial=%v final=%q", partial, finalText)
	}
	t.Logf("partial_before_final=%v final=%s", partial, finalText)
}
