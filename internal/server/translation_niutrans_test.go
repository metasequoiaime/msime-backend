package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNiuTransTranslation(t *testing.T) {
	s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/text/translate" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded; charset=utf-8" {
			t.Error("invalid NiuTrans request")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("from") != "zh" || r.Form.Get("to") != "en" || r.Form.Get("appId") != "synthetic-app" || r.Form.Get("srcText") != "测试" {
			t.Errorf("form payload mismatch: %v", r.Form)
		}
		if r.Form.Get("authStr") != niutransAuthString("synthetic-app", "synthetic-key", r.Form.Get("from"), r.Form.Get("to"), r.Form.Get("timestamp"), r.Form.Get("srcText")) {
			t.Error("authStr mismatch")
		}
		if r.Form.Has("apikey") || r.Header.Get("Authorization") != "" {
			t.Error("provider key leaked into request")
		}
		_, _ = io.WriteString(w, `{"tgtText":"test"}`)
	})
	s.config.Translation = TranslationEndpoint{Provider: "niutrans", Endpoint: Endpoint{URL: s.config.Chat.URL + "/v2/text/translate"}, appID: "synthetic-app", apiKey: "synthetic-key"}
	w := call(s, "POST", "/v1/translate", `{"text":"测试","source_lang":"ZH","target_lang":"EN"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"data":"test"`) {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestNiuTransRejectsProviderErrorsAndMalformedResults(t *testing.T) {
	for _, response := range []string{`{"errorCode":"13002","errorMsg":"private-details","tgtText":"test"}`, `{"errorCode":13002,"tgtText":"test"}`, `{"tgtText":" "}`, `{"tgtText":42}`, `{}`} {
		t.Run(response, func(t *testing.T) {
			s := fixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, response) })
			s.config.Translation = TranslationEndpoint{Provider: "niutrans", Endpoint: Endpoint{URL: s.config.Chat.URL + "/v2/text/translate"}, appID: "synthetic-app", apiKey: "synthetic-key"}
			w := call(s, "POST", "/v1/translate", `{"text":"测试","source_lang":"zh","target_lang":"en"}`)
			if w.Code != 502 || strings.Contains(w.Body.String(), "private-details") {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestNiuTransAuthStringUsesRawValues(t *testing.T) {
	if got, want := niutransAuthString("app-id", "api-key", "en", "zh", "1700000000000", "hello world"), "2884b18a960c7d503ff510436a5abe99"; got != want {
		t.Fatalf("authStr mismatch: got %s want %s", got, want)
	}
}
