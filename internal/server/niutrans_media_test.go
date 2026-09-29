package server

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNiuTransDocumentUploadAndStatus(t *testing.T) {
	s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/doc/translate/upload" {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			if r.FormValue("appId") != "synthetic-doc-app" || r.FormValue("from") != "zh" || r.FormValue("to") != "en" {
				t.Fatalf("form=%v", r.Form)
			}
			if r.FormValue("authStr") != niuTransAuth(map[string]string{"appId": "synthetic-doc-app", "from": "zh", "to": "en", "timestamp": r.FormValue("timestamp")}, "synthetic-key") {
				t.Fatal("signature mismatch")
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			body, _ := io.ReadAll(file)
			if string(body) != "synthetic document" {
				t.Fatalf("file=%q", body)
			}
			_, _ = io.WriteString(w, `{"code":200,"data":{"fileNo":"file-1"}}`)
			return
		}
		if r.URL.Path == "/v2/doc/translate/status/file-1" {
			if r.URL.Query().Get("appId") != "synthetic-doc-app" || r.URL.Query().Get("authStr") == "" {
				t.Fatal(r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"code":200,"data":{"transStatus":105}}`)
			return
		}
		t.Fatalf("unexpected upstream path %s", r.URL.Path)
	})
	s.config.NiuTrans.Document = NiuTransEndpoint{URL: s.config.Chat.URL + "/v2/doc/translate/upload", appID: "synthetic-doc-app", apiKey: "synthetic-key"}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("from", "zh")
	_ = mw.WriteField("to", "en")
	file, _ := mw.CreateFormFile("file", "private.docx")
	_, _ = file.Write([]byte("synthetic document"))
	_ = mw.Close()
	w := callMultipart(s, http.MethodPost, "/v1/niutrans/documents", mw.FormDataContentType(), body.Bytes())
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "file-1") {
		t.Fatalf("upload status=%d body=%s", w.Code, w.Body.String())
	}
	w = call(s, http.MethodGet, "/v1/niutrans/documents/file-1", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "105") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func callMultipart(s *Server, method, path, contentType string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
