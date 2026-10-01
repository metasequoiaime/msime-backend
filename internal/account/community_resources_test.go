package account

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/metasequoiaime/MSIME-Backend/internal/engine"
)

func TestResourceValidation(t *testing.T) {
	a := &Service{}
	for _, c := range []ResourceContent{{Prompt: ""}, {Prompt: strings.Repeat("字", 2001)}, {Prompt: "test\x00"}, {Prompt: "test", Entries: []SharedWord{{}}}} {
		if _, err := a.validateResource(context.Background(), "reply", c); err == nil {
			t.Fatal("invalid prompt accepted")
		}
	}
	if _, err := a.validateResource(context.Background(), "reply", ResourceContent{Prompt: "简短回答\n保持礼貌"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []ResourceContent{{}, {Entries: make([]SharedWord, 129)}, {Entries: []SharedWord{{Kind: "unknown"}}}, {Prompt: "no", Entries: []SharedWord{{Kind: "pinyin"}}}} {
		if _, err := a.validateResource(context.Background(), "dictionary", c); err == nil {
			t.Fatal("invalid dictionary accepted")
		}
	}
}
func TestResourcePublishVersionSaveRatingIsolation(t *testing.T) {
	store := testStore(t)
	owner := complete(t, store, Identity{"apple", "resource-owner"})
	reader := complete(t, store, Identity{"apple", "resource-reader"})
	mux := http.NewServeMux()
	Mount(mux, &Service{store: store})
	call := func(method, path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	id := "ab334455-1234-1234-1234-123456789abc"
	path := "/v1/community/resources/" + id
	body := `{"id":"` + id + `","kind":"reply","name":"职场回复","description":"示例","content":{"prompt":"简短自然"},"revision":0}`
	check := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("got %d want %d: %s", w.Code, status, w.Body.String())
		}
	}
	check(call("POST", "/v1/community/resources", body, ""), 401)
	check(call("POST", "/v1/community/resources", body, owner.AccessToken), 201)
	check(call("POST", "/v1/community/resources", body, owner.AccessToken), 200)
	check(call("POST", "/v1/community/resources", body, reader.AccessToken), 409)
	// 登录即可评分，不需要先收藏。
	check(call("PUT", path+"/rating", `{"stars":5}`, reader.AccessToken), 200)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := call("PUT", path+"/save", `{"saved":true}`, reader.AccessToken)
			if w.Code != 200 {
				t.Error(w.Code)
			}
		}()
	}
	wg.Wait()
	check(call("PUT", path+"/rating", `{"stars":4}`, reader.AccessToken), 200)
	check(call("PUT", path+"/rating", `{"stars":5}`, owner.AccessToken), 403)
	check(call("DELETE", path, "", reader.AccessToken), 404)
	change := strings.Replace(strings.Replace(body, "简短自然", "友好清晰", 1), `"revision":0`, `"revision":1`, 1)
	check(call("POST", "/v1/community/resources", change, owner.AccessToken), 200)
	check(call("POST", "/v1/community/resources", change, owner.AccessToken), 200)
	check(call("POST", "/v1/community/resources", strings.Replace(change, "友好清晰", "过期编辑", 1), owner.AccessToken), 409)
	w := call("GET", path, "", reader.AccessToken)
	check(w, 200)
	var item CommunityResource
	if json.Unmarshal(w.Body.Bytes(), &item) != nil || item.Revision != 2 || item.Saves != 1 || !item.Saved || item.Owned || item.MyRating != 4 {
		t.Fatal(w.Body.String())
	}
	w = call("GET", "/v1/community/resources?kind=reply&scope=saved", "", reader.AccessToken)
	check(w, 200)
	if !strings.Contains(w.Body.String(), id) {
		t.Fatal("missing saved")
	}
	w = call("GET", "/v1/community/resources?kind=reply&scope=mine", "", reader.AccessToken)
	check(w, 200)
	if strings.Contains(w.Body.String(), id) {
		t.Fatal("owner leak")
	}
	check(call("GET", "/v1/community/resources?kind=reply&scope=saved", "", ""), 401)
	check(call("GET", "/v1/community/resources?kind=invalid", "", ""), 400)
	check(call("PUT", path+"/save", `{"saved":false}`, reader.AccessToken), 200)
	check(call("PUT", path+"/save", `{"saved":true}`, reader.AccessToken), 200)
	if err := store.DeleteUser(context.Background(), owner.User.ID); err != nil {
		t.Fatal(err)
	}
	check(call("GET", path, "", ""), 404)
	check(call("PUT", path+"/save", `{"saved":true}`, reader.AccessToken), 404)
}
func TestResourceDictionaryUsesNativeValidation(t *testing.T) {
	binary := os.Getenv("MSIME_ENGINE_TEST_BINARY")
	if binary == "" {
		t.Skip("native engine required")
	}
	a := &Service{engine: engine.Config{Binary: binary, Resources: os.Getenv("MSIME_ENGINE_TEST_RESOURCES")}}
	input := ResourceContent{Entries: []SharedWord{{Kind: "pinyin", Code: "ni hao", Word: "你好", Weight: 10}}}
	result, err := a.validateResource(context.Background(), "dictionary", input)
	if err != nil || len(result.Entries) != 1 || result.Entries[0].Code != "ni'hao" {
		t.Fatal(result, err)
	}
	input.Entries = append(input.Entries, input.Entries[0])
	if _, err = a.validateResource(context.Background(), "dictionary", input); err == nil {
		t.Fatal("accepted normalized duplicate")
	}
	input.Entries = []SharedWord{{Kind: "pinyin", Code: "not-valid", Word: "你好", Weight: 10}}
	if _, err = a.validateResource(context.Background(), "dictionary", input); err == nil {
		t.Fatal("accepted invalid pinyin")
	}
}

func TestStarterResourcesUseValidContent(t *testing.T) {
	raw, err := os.ReadFile("../../assets/community-starter-resources.json")
	if err != nil {
		t.Fatal(err)
	}
	var items []struct {
		Slug, Kind, Name, Description string
		Content                       ResourceContent
	}
	if err = json.Unmarshal(raw, &items); err != nil || len(items) != 5 {
		t.Fatal(err)
	}
	binary := os.Getenv("MSIME_ENGINE_TEST_BINARY")
	a := &Service{engine: engine.Config{Binary: binary, Resources: os.Getenv("MSIME_ENGINE_TEST_RESOURCES")}}
	seen := map[string]bool{}
	for _, item := range items {
		if seen[item.Slug] || !resourceText(item.Name, 1, 32, false) || !resourceText(item.Description, 0, 280, true) {
			t.Fatal("invalid starter metadata")
		}
		seen[item.Slug] = true
		if item.Kind == "dictionary" && binary == "" {
			continue
		}
		if _, err = a.validateResource(context.Background(), item.Kind, item.Content); err != nil {
			t.Fatal(item.Slug, err)
		}
	}
}
