package server

import (
	"bytes"
	"crypto/md5" // #nosec G501 -- NiuTrans v2 requires MD5 for authStr.
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/metasequoiaime/MSIME-Backend/internal/contract"
)

// niutransAuthString is the NiuTrans v2 authStr algorithm. The signature uses
// raw UTF-8 parameter values; URL encoding is applied only to the form body.
func niutransAuthString(appID, apiKey, source, target, timestamp, text string) string {
	canonical := "apikey=" + apiKey + "&appId=" + appID + "&from=" + source + "&srcText=" + text + "&timestamp=" + timestamp + "&to=" + target
	checksum := md5.Sum([]byte(canonical)) // #nosec G401 -- required by the provider protocol.
	return hex.EncodeToString(checksum[:])
}

func (s *Server) translateNiuTrans(w http.ResponseWriter, r *http.Request, v translationRequest) {
	source, target := strings.ToLower(v.Source), strings.ToLower(v.Target)
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	form := url.Values{
		"from":      {source},
		"to":        {target},
		"appId":     {s.config.Translation.appID},
		"timestamp": {timestamp},
		"srcText":   {v.Text},
	}
	form.Set("authStr", niutransAuthString(s.config.Translation.appID, s.config.Translation.apiKey, source, target, timestamp, v.Text))
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.config.Translation.URL, bytes.NewBufferString(form.Encode()))
	if err != nil {
		upstreamError(w, r, err)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("Accept", "application/json")
	body, err := s.doUpstream(req)
	var result map[string]json.RawMessage
	if err != nil || json.Unmarshal(body, &result) != nil || result == nil {
		upstreamError(w, r, err)
		return
	}
	if _, hasError := result["errorCode"]; hasError {
		upstreamError(w, r, nil)
		return
	}
	if _, hasError := result["errorMsg"]; hasError {
		upstreamError(w, r, nil)
		return
	}
	var translated string
	if json.Unmarshal(result["tgtText"], &translated) != nil || !bounded(translated, contract.OutputTextBytes) {
		upstreamError(w, r, err)
		return
	}
	respond(w, 200, map[string]any{"code": 200, "data": translated})
}
