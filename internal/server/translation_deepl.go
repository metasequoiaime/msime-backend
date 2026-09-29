package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/metasequoiaime/MSIME-Backend/internal/contract"
)

func (s *Server) translateDeepL(w http.ResponseWriter, r *http.Request, v translationRequest, e TranslationEndpoint) {
	form := url.Values{}
	for _, text := range v.list() {
		form.Add("text", text)
	}
	if !strings.EqualFold(v.Source, "auto") {
		form.Set("source_lang", strings.ToUpper(v.Source))
	}
	form.Set("target_lang", strings.ToUpper(v.Target))
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, e.URL, strings.NewReader(form.Encode()))
	if err != nil {
		upstreamError(w, r, err)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "DeepL-Auth-Key "+e.token)
	body, err := s.doUpstream(req)
	var result struct {
		Translations []struct {
			Text string `json:"text"`
		} `json:"translations"`
	}
	if err != nil || json.Unmarshal(body, &result) != nil || len(result.Translations) != len(v.list()) {
		upstreamError(w, r, err)
		return
	}
	translated := make([]string, len(result.Translations))
	for i, item := range result.Translations {
		if !bounded(item.Text, contract.OutputTextBytes) {
			upstreamError(w, r, err)
			return
		}
		translated[i] = item.Text
	}
	respondTranslations(w, v, translated)
}
