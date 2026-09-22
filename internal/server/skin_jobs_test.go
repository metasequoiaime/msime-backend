package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func createArtworkJob(t *testing.T, s *Server) string {
	t.Helper()
	w := call(s, "POST", "/v1/skins/jobs", `{"prompt":"原创森林"}`)
	if w.Code != 202 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.ID) != 48 {
		t.Fatal("missing job ID")
	}
	return "/v1/skins/jobs/" + out.ID
}

func TestSkinJobReturnsBeforeUpstreamAndBindsOwner(t *testing.T) {
	release := make(chan struct{})
	var encoded bytes.Buffer
	_ = png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 32, 24)))
	s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		respond(w, 200, map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(encoded.Bytes())}}})
	})
	t.Cleanup(s.Close)
	s.config.Images = s.config.Chat
	path := createArtworkJob(t, s) // Upstream cannot finish before this returns.
	if w := call(s, "GET", path, ""); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"running"`)) {
		t.Fatal(w.Code, w.Body.String())
	}
	s.config.Clients = append(s.config.Clients, Client{ID: "other", token: "other-token", RequestsPerMinute: 100})
	for _, method := range []string{"GET", "DELETE"} {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer other-token")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("cross-owner %s: %d", method, w.Code)
		}
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		w := call(s, "GET", path, "")
		if bytes.Contains(w.Body.Bytes(), []byte(`"succeeded"`)) {
			var out struct {
				Artwork struct {
					Width int    `json:"width"`
					Image string `json:"b64_json"`
				} `json:"artwork"`
			}
			if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Artwork.Width != 32 || out.Artwork.Image != base64.StdEncoding.EncodeToString(encoded.Bytes()) {
				t.Fatal("invalid artwork")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish", w.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if w := call(s, "DELETE", path, ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w := call(s, "GET", path, ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestSkinJobCapacityCancelExpiryAndShutdown(t *testing.T) {
	entered := make(chan struct{}, 8)
	s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		entered <- struct{}{}
		<-r.Context().Done()
	})
	t.Cleanup(s.Close)
	s.config.Images = s.config.Chat
	paths := []string{createArtworkJob(t, s), createArtworkJob(t, s), createArtworkJob(t, s)}
	for range paths {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("worker not started")
		}
	}
	if w := call(s, "POST", "/v1/skins/jobs", `{"prompt":"fourth"}`); w.Code != 503 {
		t.Fatal("quota", w.Code)
	}
	if w := call(s, "DELETE", paths[0], ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	s.mu.Lock()
	s.skinJobs[paths[1][len("/v1/skins/jobs/"):]].expires = time.Now().Add(-time.Second)
	s.mu.Unlock()
	if w := call(s, "GET", paths[1], ""); w.Code != 404 {
		t.Fatal("expired", w.Code)
	}
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not cancel workers")
	}
	s.mu.Lock()
	active := s.skinActive
	s.mu.Unlock()
	if active != 0 {
		t.Fatal("leaked workers", active)
	}
	if w := call(s, "POST", "/v1/skins/jobs", `{"prompt":"closed"}`); w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestSkinJobInvalidImageIsFailedWithoutLeakingUpstream(t *testing.T) {
	s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]any{"data": []any{map[string]string{"url": "https://private.example/token"}}})
	})
	t.Cleanup(s.Close)
	s.config.Images = s.config.Chat
	path := createArtworkJob(t, s)
	s.skinWorkers.Wait()
	w := call(s, "GET", path, "")
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"failed"`)) || bytes.Contains(w.Body.Bytes(), []byte("private.example")) {
		t.Fatal(w.Code, w.Body.String())
	}
}

// 失败的任务必须说出原因。
//
// 此前它只回 state="failed",而具体是哪一种(图像无效、上游拒绝、上游超时)在写入任务时就被丢掉了。
// 客户端拿不到原因,于是自己编了一个 502 上报;服务端日志里也没有。这条断言把原因留在响应里 ——
// 同时仍然不许泄露上游地址,那是这一组测试原本就在守的东西。
func TestFailedSkinJobReportsItsReason(t *testing.T) {
	s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]any{"data": []any{map[string]string{"url": "https://private.example/token"}}})
	})
	t.Cleanup(s.Close)
	s.config.Images = s.config.Chat
	path := createArtworkJob(t, s)
	s.skinWorkers.Wait()
	w := call(s, "GET", path, "")
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"failed"`)) {
		t.Fatal(w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"reason":"invalid_skin_artwork"`)) {
		t.Fatal("失败原因没有带出来:", w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("private.example")) {
		t.Fatal("上游地址泄露了:", w.Body.String())
	}
}

// 成功的任务不该多出一个空的 reason 字段 —— 它只在失败时才有意义。
func TestSucceededSkinJobCarriesNoReason(t *testing.T) {
	var encoded bytes.Buffer
	_ = png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 32, 24)))
	s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(encoded.Bytes())}}})
	})
	t.Cleanup(s.Close)
	s.config.Images = s.config.Chat
	path := createArtworkJob(t, s)
	s.skinWorkers.Wait()
	w := call(s, "GET", path, "")
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"succeeded"`)) {
		t.Fatal(w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte(`"reason"`)) {
		t.Fatal("成功的任务不该带 reason:", w.Body.String())
	}
}
