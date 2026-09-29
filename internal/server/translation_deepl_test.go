package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDeepLTranslation(t *testing.T) {
	t.Setenv("TEST_DEEPL_KEY", "synthetic-deepl-key:fx")
	s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "DeepL-Auth-Key synthetic-deepl-key:fx" {
			t.Fatal("DeepL credential was not isolated")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("source_lang") != "ZH" || r.Form.Get("target_lang") != "EN" || r.Form.Get("text") != "测试" {
			t.Fatalf("unexpected form: %v", r.Form)
		}
		_, _ = io.WriteString(w, `{"translations":[{"text":"test"}]}`)
	})
	s.config.Translation = TranslationEndpoint{Provider: "deepl", Endpoint: Endpoint{URL: s.config.Chat.URL + "/v2/translate", TokenEnv: "TEST_DEEPL_KEY"}}
	s.config.Translation.token = "synthetic-deepl-key:fx"
	w := call(s, "POST", "/v1/translate", `{"text":"测试","source_lang":"zh","target_lang":"en"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"data":"test"`) {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestDeepLRejectsProviderErrorsAndMalformedResults(t *testing.T) {
	for _, response := range []string{`{"message":"private details"}`, `{"translations":[]}`, `{"translations":[{}]}`, `invalid`} {
		t.Run(response, func(t *testing.T) {
			s := fixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, response) })
			s.config.Translation = TranslationEndpoint{Provider: "deepl", Endpoint: Endpoint{URL: s.config.Chat.URL + "/v2/translate", TokenEnv: "TEST_DEEPL_KEY"}}
			s.config.Translation.token = "synthetic-deepl-key:fx"
			w := call(s, "POST", "/v1/translate", `{"text":"测试","source_lang":"zh","target_lang":"en"}`)
			if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "private details") {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
}
