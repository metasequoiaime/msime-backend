package server

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestChatAndCatalogBoundaryResponses(t *testing.T) {
	s := fixture(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid input reached upstream") })
	for _, extra := range []string{`"temperature":-0.1`, `"temperature":2.1`, `"response_format":{"type":"xml"}`, `"thinking":{"type":"enabled"}`, `"enable_thinking":true`} {
		w := call(s, "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hello"}],`+extra+`}`)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, message := range []string{`{"role":"tool","content":"hello"}`, `{"role":"user","content":""}`} {
		w := call(s, "POST", "/v1/chat/completions", `{"messages":[`+message+`]}`)
		if w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	for _, path := range []string{"emoji?unexpected=1", "emoji?limit=bad", "emoji?offset=bad", "emoji?limit=201", "emoji?offset=-1"} {
		w := call(s, "GET", "/v1/catalog/"+path, "")
		if w.Code != 400 {
			t.Fatal(path, w.Code)
		}
	}
	if w := call(s, "GET", "/v1/catalog/missing", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	s.config.Chat.URL = ""
	if w := call(s, "GET", "/v1/models", ""); w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestRateBucketCapacityAndRecovery(t *testing.T) {
	now := time.Now()
	s := &Server{buckets: map[string]bucket{}}
	for i := 0; i < 10001; i++ {
		s.buckets[fmt.Sprint(i)] = bucket{tokens: 1, updated: now}
	}
	if s.allow(Client{ID: "new", RequestsPerMinute: 10}, now) {
		t.Fatal("capacity exceeded")
	}
	if !s.allow(Client{ID: "0", RequestsPerMinute: 10}, now) {
		t.Fatal("existing client blocked")
	}
	if !s.allow(Client{ID: "new", RequestsPerMinute: 10}, now.Add(11*time.Minute)) || len(s.buckets) != 1 {
		t.Fatal("expired clients were not pruned")
	}
	// Capacity is per replica as well: a divided share of 10 over 4 replicas holds ceil(10/4)=3 tokens and recovers at 3 per minute.
	s.config.Replicas = 4
	c := Client{ID: "divided", RequestsPerMinute: 10}
	for i := 0; i < 3; i++ {
		if !s.allowPrincipal(c, now) {
			t.Fatal("per-replica capacity too small", i)
		}
	}
	if s.allowPrincipal(c, now) || !s.allowPrincipal(c, now.Add(20*time.Second)) {
		t.Fatal("per-replica capacity or recovery wrong")
	}
}

func TestTranscriptionMultipartBoundariesAndProviderFormats(t *testing.T) {
	var response string
	s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		if r.FormValue("language") != "en" || r.FormValue("model") != "server-model" {
			t.Error("incorrect upstream fields")
		}
		io.WriteString(w, response)
	})
	request := func(fields [][2]string, audio []byte) *httptest.ResponseRecorder {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		if audio != nil {
			f, err := mw.CreateFormFile("file", "audio.wav")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.Write(audio); err != nil {
				t.Fatal(err)
			}
		}
		for _, v := range fields {
			if err := mw.WriteField(v[0], v[1]); err != nil {
				t.Fatal(err)
			}
		}
		if err := mw.Close(); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/v1/audio/transcriptions", &body)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	for _, fields := range [][][2]string{{{"model", "a"}, {"model", "b"}}, {{"model", strings.Repeat("x", 257)}}, {{"language", "1"}}, {{"response_format", "text"}}, {{"unknown", "value"}}} {
		if w := request(fields, testWAV()); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := request(nil, []byte{}); w.Code != 413 {
		t.Fatal(w.Code)
	}
	for _, tc := range []struct {
		body   string
		status int
	}{{`{"transcription":"hello"}`, 200}, {`{"result":{"text":"hello"}}`, 200}, {`{}`, 502}, {`invalid`, 502}} {
		response = tc.body
		w := request([][2]string{{"language", "en"}}, testWAV())
		if w.Code != tc.status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest("POST", "/v1/audio/transcriptions", strings.NewReader("truncated"))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=missing")
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}
