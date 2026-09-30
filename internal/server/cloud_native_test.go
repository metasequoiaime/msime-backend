package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"testing"
)

func TestCloudNativeWholeInputCorrection(t *testing.T) {
	binary, resources := os.Getenv("MSIME_ENGINE_TEST_BINARY"), os.Getenv("MSIME_ENGINE_TEST_RESOURCES")
	if binary == "" || resources == "" {
		t.Skip("需要真实 Engine 与发布词库")
	}
	for _, tc := range []struct{ text, upstream, expected string }{
		{"zhonguo", `["SUCCESS",[["zhonguo",["中UO","中"],[],{"matched_length":[7,4]}]]]`, "中国"},
		{"zhon'guo", `["SUCCESS",[["zhon'guo",["中哦你过"],[],{"matched_length":[8]}]]]`, "中国"},
		{"ni'hao", `["SUCCESS",[["ni'hao",["你号"],[],{"matched_length":[6]}]]]`, "你号"},
		{"nihao", `["SUCCESS",[["nihao",["你"],[],{"matched_length":[2]}]]]`, "你好"},
		{"zzzzzzzzzz", `["SUCCESS",[["zzzzzzzzzz",[],[],{}]]]`, ""},
	} {
		t.Run(tc.text, func(t *testing.T) {
			s := fixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, tc.upstream) })
			s.config.Engine.Binary = binary
			s.config.Engine.Resources = resources
			w := call(s, "GET", "/v1/cloud/candidates?scheme=pinyin&limit=1&text="+url.QueryEscape(tc.text), "")
			var result struct {
				Candidates []string `json:"candidates"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.expected == "" {
				if len(result.Candidates) != 0 {
					t.Fatal(result)
				}
			} else if len(result.Candidates) != 1 || result.Candidates[0] != tc.expected {
				t.Fatal(result)
			}
		})
	}
}

func TestCloudNativeShippedWords(t *testing.T) {
	binary, resources := os.Getenv("MSIME_ENGINE_TEST_BINARY"), os.Getenv("MSIME_ENGINE_TEST_RESOURCES")
	if binary == "" || resources == "" {
		t.Skip("需要真实 Engine 与发布词库")
	}
	s := fixture(t, nil)
	s.config.Engine.Binary = binary
	s.config.Engine.Resources = resources
	entries := []wordSubmissionEntry{{"测试", "ce'shi"}, {"测试", "ce'si"}, {"未来可期", "wei'lai'ke'qi"}, {"中华人民共和国", "zhong'hua'ren'min'gong'he'guo"}, {"测测测测测测测测", "ce'ce'ce'ce'ce'ce'ce'ce"}}
	err := s.shippedWords(context.Background(), entries)
	var listed alreadyListedError
	if !errors.As(err, &listed) || len(listed) != 2 || listed[0] != 0 || listed[1] != 3 {
		t.Fatal(err, listed)
	}
}

func TestCloudDoesNotRequireNativeFallback(t *testing.T) {
	for _, body := range []string{`["SUCCESS",[["nihao",[],[],{}]]]`, `["SUCCESS",[["nihao",["你好"],[],{}]]]`} {
		s := fixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) })
		// Native input is optional; successful upstream responses remain usable without it.
		w := call(s, "GET", "/v1/cloud/candidates?text=nihao", "")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	s := fixture(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 503) })
	w := call(s, "GET", "/v1/cloud/candidates?text=nihao", "")
	if w.Code != 502 {
		t.Fatal("upstream failure must remain visible", w.Code, w.Body.String())
	}
}
