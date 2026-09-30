package account

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func apiRequest(t *testing.T, handler http.Handler, method, path, body, token string, status int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	return w
}

func TestAccountHTTPProfileRefreshLogoutDelete(t *testing.T) {
	db := testStore(t)
	a := &Service{store: db}
	mux := http.NewServeMux()
	Mount(mux, a)
	first := complete(t, db, Identity{"email", "profile@example.test"})
	other := complete(t, db, Identity{"email", "other@example.test"})
	apiRequest(t, mux, "PATCH", "/v1/users/me", `{"display_name":"  测试昵称  "}`, first.AccessToken, 204)
	profile := apiRequest(t, mux, "GET", "/v1/users/me", "", first.AccessToken, 200)
	var body struct {
		User       User       `json:"user"`
		Identities []Identity `json:"identities"`
	}
	if err := json.Unmarshal(profile.Body.Bytes(), &body); err != nil || body.User.ID != first.User.ID || body.User.DisplayName != "测试昵称" || len(body.Identities) != 1 {
		t.Fatal(profile.Body.String(), err)
	}
	badName, _ := json.Marshal(map[string]string{"display_name": strings.Repeat("长", 65)})
	apiRequest(t, mux, "PATCH", "/v1/users/me", string(badName), first.AccessToken, 400)
	refreshBody, _ := json.Marshal(map[string]string{"refresh_token": first.RefreshToken})
	w := apiRequest(t, mux, "POST", "/v1/auth/refresh", string(refreshBody), "", 200)
	var refreshed Tokens
	if err := json.Unmarshal(w.Body.Bytes(), &refreshed); err != nil || refreshed.User.ID != first.User.ID || refreshed.RefreshToken == first.RefreshToken || refreshed.AccessToken == first.AccessToken {
		t.Fatal("tokens not rotated", err)
	}
	apiRequest(t, mux, "GET", "/v1/users/me", "", first.AccessToken, 401)
	apiRequest(t, mux, "GET", "/v1/users/me", "", refreshed.AccessToken, 200)
	apiRequest(t, mux, "POST", "/v1/auth/refresh", string(refreshBody), "", 401)
	apiRequest(t, mux, "GET", "/v1/users/me", "", refreshed.AccessToken, 401)
	second := complete(t, db, Identity{"email", "profile@example.test"})
	third := complete(t, db, Identity{"email", "profile@example.test"})
	apiRequest(t, mux, "POST", "/v1/auth/logout", `{"all":false}`, second.AccessToken, 204)
	apiRequest(t, mux, "GET", "/v1/users/me", "", second.AccessToken, 401)
	apiRequest(t, mux, "GET", "/v1/users/me", "", third.AccessToken, 200)
	fourth := complete(t, db, Identity{"email", "profile@example.test"})
	apiRequest(t, mux, "POST", "/v1/auth/logout", `{"all":true}`, third.AccessToken, 204)
	apiRequest(t, mux, "GET", "/v1/users/me", "", fourth.AccessToken, 401)
	fresh := complete(t, db, Identity{"email", "profile@example.test"})
	apiRequest(t, mux, "DELETE", "/v1/users/me", "", fresh.AccessToken, 204)
	apiRequest(t, mux, "GET", "/v1/users/me", "", fresh.AccessToken, 401)
	apiRequest(t, mux, "GET", "/v1/users/me", "", other.AccessToken, 200)
	apiRequest(t, mux, "POST", "/v1/auth/refresh", `{"refresh_token":"missing"}`, "", 401)
}

// Walk the published operations so new account APIs cannot silently miss their
// authentication, disabled-service and router method contracts.
func TestEveryAccountRouteAuthenticationAndDisabledService(t *testing.T) {
	raw, err := os.ReadFile("../server/swagger/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err = json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	db := testStore(t)
	a := &Service{store: db}
	mux := http.NewServeMux()
	Mount(mux, a)
	disabled := http.NewServeMux()
	Mount(disabled, nil)
	n := 0
	for path, methods := range spec.Paths {
		// The website word form is mounted by the server package and covered by its word submission tests.
		if !IsPath(path) || path == "/v1/community/word-submissions" {
			continue
		}
		concrete := strings.NewReplacer("{id}", "missing", "{kind}", "pinyin").Replace(path)
		for method := range methods {
			if method != "get" && method != "post" && method != "put" && method != "patch" && method != "delete" {
				continue
			}
			n++
			expected := 401
			// Community reads are public, except the author's own sync listing.
			if method == "get" && strings.HasPrefix(path, "/v1/community/") && path != "/v1/community/candidate-skins/sync" {
				if strings.Contains(path, "{id}") {
					expected = 404
				} else {
					expected = 200
				}
				if path == "/v1/community/resources" {
					concrete += "?kind=dictionary"
				}
			}
			t.Run(method+" "+path, func(t *testing.T) {
				verb := strings.ToUpper(method)
				apiRequest(t, disabled, verb, concrete, `{}`, "", 503)
				apiRequest(t, mux, "TRACE", concrete, `{}`, "", 405)
				if path == "/v1/auth/providers" || path == "/v1/auth/challenges" || path == "/v1/auth/login" || path == "/v1/auth/refresh" {
					return
				}
				for i, token := range []string{"", "invalid-client-token"} {
					// Distinct peers keep this routing test independent of the shared quota test.
					r := httptest.NewRequest(verb, concrete, strings.NewReader(`{}`))
					r.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", i+1)
					r.Header.Set("Content-Type", "application/json")
					if token != "" {
						r.Header.Set("Authorization", "Bearer "+token)
					}
					w := httptest.NewRecorder()
					mux.ServeHTTP(w, r)
					if w.Code != expected {
						t.Fatalf("unauthenticated route returned %d: %s", w.Code, w.Body.String())
					}
				}
			})
		}
	}
	if n == 0 {
		t.Fatal("account route inventory empty")
	}
}

func TestProviderDiscoveryAndMalformedAuthenticationBodies(t *testing.T) {
	db := testStore(t)
	a := &Service{store: db, config: Config{Google: OIDCConfig{ClientIDs: []string{"test-client"}}, Email: MailConfig{From: "test@example.test"}}}
	mux := http.NewServeMux()
	Mount(mux, a)
	w := apiRequest(t, mux, "GET", "/v1/auth/providers", "", "", 200)
	var response struct {
		Providers map[string]bool `json:"providers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Providers) != 6 || !response.Providers["google"] || !response.Providers["email"] || response.Providers["phone"] || response.Providers["anonymous"] {
		t.Fatal(w.Body.String(), err)
	}
	user := complete(t, db, Identity{"email", "malformed@example.test"})
	for _, path := range []string{"/v1/auth/challenges", "/v1/auth/login", "/v1/auth/refresh", "/v1/auth/logout"} {
		for _, body := range []string{`{`, `{} {}`, `{"unknown":true}`} {
			apiRequest(t, mux, "POST", path, body, user.AccessToken, 400)
		}
		r := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer "+user.AccessToken)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 415 {
			t.Fatal(path, w.Code)
		}
	}
	apiRequest(t, mux, "POST", "/v1/auth/challenges", `{"provider":"unknown"}`, "", 503)
	apiRequest(t, mux, "POST", "/v1/auth/login", `{"challenge_id":"missing","credential":"invalid"}`, "", 401)
}

func TestClipboardClearAndCommunityResourceDeleteHTTP(t *testing.T) {
	db := testStore(t)
	one := complete(t, db, Identity{"email", "clear-one@example.test"})
	two := complete(t, db, Identity{"email", "clear-two@example.test"})
	mux := http.NewServeMux()
	Mount(mux, &Service{store: db})
	for _, token := range []string{one.AccessToken, two.AccessToken} {
		apiRequest(t, mux, "PUT", "/v1/users/me/clipboard/settings", `{"enabled":true}`, token, 200)
		apiRequest(t, mux, "POST", "/v1/users/me/clipboard", `{"text":"retained by its owner"}`, token, 200)
	}
	apiRequest(t, mux, "DELETE", "/v1/users/me/clipboard", "", one.AccessToken, 204)
	var response struct {
		Items []json.RawMessage `json:"items"`
	}
	w := apiRequest(t, mux, "GET", "/v1/users/me/clipboard", "", one.AccessToken, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Items) != 0 {
		t.Fatal(w.Body.String(), err)
	}
	w = apiRequest(t, mux, "GET", "/v1/users/me/clipboard", "", two.AccessToken, 200)
	if !strings.Contains(w.Body.String(), "retained by its owner") {
		t.Fatal("clear crossed owner boundary")
	}
	apiRequest(t, mux, "DELETE", "/v1/users/me/clipboard", "", one.AccessToken, 204)
	const id = "ab334455-1234-1234-1234-123456789ddd"
	path := "/v1/community/resources/" + id
	apiRequest(t, mux, "POST", "/v1/community/resources", `{"id":"`+id+`","kind":"reply","name":"Delete fixture","content":{"prompt":"Hello"},"revision":0}`, one.AccessToken, 201)
	apiRequest(t, mux, "PUT", path+"/save", `{"saved":true}`, two.AccessToken, 200)
	apiRequest(t, mux, "PUT", path+"/rating", `{"stars":4}`, two.AccessToken, 200)
	apiRequest(t, mux, "DELETE", path, "", two.AccessToken, 404)
	apiRequest(t, mux, "DELETE", path, "", one.AccessToken, 200)
	apiRequest(t, mux, "GET", path, "", "", 404)
	apiRequest(t, mux, "DELETE", path, "", one.AccessToken, 404)
	var remaining int
	if err := db.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM community_resource_saves WHERE resource_id=$1)+(SELECT count(*) FROM community_resource_ratings WHERE resource_id=$1)`, id).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("orphaned community records", remaining, err)
	}
}

func TestEveryAccountJSONBodyRejectsMalformedInput(t *testing.T) {
	raw, err := os.ReadFile("../server/swagger/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err = json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	db := testStore(t)
	user := complete(t, db, Identity{"email", "body-contract@example.test"})
	mux := http.NewServeMux()
	Mount(mux, &Service{store: db})
	count := 0
	for path, methods := range spec.Paths {
		// The website word form is mounted by the server package; its malformed-body cases are in TestWordSubmissionValidation there.
		if !IsPath(path) || path == "/v1/community/word-submissions" {
			continue
		}
		path = strings.NewReplacer("{id}", "missing", "{kind}", "pinyin").Replace(path)
		for method, rawOperation := range methods {
			if method != "post" && method != "put" && method != "patch" && method != "delete" {
				continue
			}
			var operation struct {
				RequestBody struct {
					Content map[string]json.RawMessage `json:"content"`
				} `json:"requestBody"`
			}
			if err := json.Unmarshal(rawOperation, &operation); err != nil {
				t.Fatal(err)
			}
			if operation.RequestBody.Content["application/json"] == nil {
				continue
			}
			count++
			t.Run(method+" "+path, func(t *testing.T) {
				for _, body := range []string{`{`, `{} {}`, `{"unexpected_contract_field":true}`} {
					apiRequest(t, mux, strings.ToUpper(method), path, body, user.AccessToken, 400)
				}
				r := httptest.NewRequest(strings.ToUpper(method), path, strings.NewReader(`{}`))
				r.Header.Set("Authorization", "Bearer "+user.AccessToken)
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				if w.Code != 415 {
					t.Fatal("media type accepted", w.Code, w.Body.String())
				}
			})
		}
	}
	if count == 0 {
		t.Fatal("no JSON request bodies checked")
	}
}
