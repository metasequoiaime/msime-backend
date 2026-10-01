package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/metasequoiaime/MSIME-Backend/internal/account"
)

const testTelegramToken = "123456:ABCdefGHIjklMNOpqrSTUvwxYZ012345"

// fakeTelegram is a Bot API stand-in that records sendMessage bodies and answers with status and ok.
type fakeTelegram struct {
	mu          sync.Mutex
	paths       []string
	messages    []map[string]any
	status      int
	ok          bool
	description string
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	raw, _ := io.ReadAll(r.Body)
	json.Unmarshal(raw, &body)
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	f.messages = append(f.messages, body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.status)
	json.NewEncoder(w).Encode(map[string]any{"ok": f.ok, "description": f.description})
}

func TestTelegramNoticeBroadcaster(t *testing.T) {
	s := fixture(t, nil)
	if s.noticeBroadcaster() != nil {
		t.Fatal("broadcaster without admin.telegram")
	}
	fake := &fakeTelegram{status: 200, ok: true}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s.config.Admin.Telegram = AdminTelegramConfig{ChatID: "@msime_news", APIURL: srv.URL, botToken: testTelegramToken}
	b := s.noticeBroadcaster()
	if b == nil {
		t.Fatal("no broadcaster with admin.telegram")
	}
	ctx := context.Background()
	if err := b.BroadcastNotice(ctx, account.Notice{ID: 1, Title: "Windows v0.5.5 已发布", Body: "修复了若干问题", Channels: []string{"telegram"}}); err != nil {
		t.Fatal(err)
	}
	if fake.paths[0] != "POST /bot"+testTelegramToken+"/sendMessage" || fake.messages[0]["chat_id"] != "@msime_news" || fake.messages[0]["text"] != "Windows v0.5.5 已发布\n\n修复了若干问题" {
		t.Fatal(fake.paths, fake.messages)
	}
	// A message over Telegram's 4096-character limit is cut on a character boundary.
	if err := b.BroadcastNotice(ctx, account.Notice{Title: "长", Body: strings.Repeat("水杉", 3000)}); err != nil {
		t.Fatal(err)
	}
	text := fake.messages[1]["text"].(string)
	if telegramLength(text) != telegramMessageMax || utf8.RuneCountInString(text) != telegramMessageMax || !strings.HasSuffix(text, "…") {
		t.Fatal(utf8.RuneCountInString(text))
	}
	// Telegram counts UTF-16 code units, so a body of emoji is cut at half as many characters and never splits a surrogate pair.
	if err := b.BroadcastNotice(ctx, account.Notice{Title: "表情", Body: strings.Repeat("🎉", 3000)}); err != nil {
		t.Fatal(err)
	}
	text = fake.messages[2]["text"].(string)
	if n := telegramLength(text); n > telegramMessageMax || n < telegramMessageMax-1 || !utf8.ValidString(text) || !strings.HasSuffix(text, "🎉…") {
		t.Fatal(n)
	}
	if exact := strings.Repeat("a", telegramMessageMax); telegramText(account.Notice{Title: exact}) != exact {
		t.Fatal("a message at the limit was cut")
	}

	// Rejections and transport failures are errors that never carry the bot token.
	fake.status, fake.ok = 200, false
	if err := b.BroadcastNotice(ctx, account.Notice{Title: "x"}); err == nil || strings.Contains(err.Error(), testTelegramToken) {
		t.Fatal(err)
	}
	// The Bot API's description reaches the error for the operator, with the token redacted should it ever be echoed.
	fake.status, fake.description = 403, "Forbidden: bot "+testTelegramToken+" is not a member of the channel chat"
	if err := b.BroadcastNotice(ctx, account.Notice{Title: "x"}); err == nil || strings.Contains(err.Error(), testTelegramToken) || !strings.Contains(err.Error(), "not a member of the channel chat") {
		t.Fatal(err)
	}
	fake.description = ""
	fake.status = 403
	if err := b.BroadcastNotice(ctx, account.Notice{Title: "x"}); err == nil || strings.Contains(err.Error(), testTelegramToken) {
		t.Fatal(err)
	}
	srv.Close()
	if err := b.BroadcastNotice(ctx, account.Notice{Title: "x"}); err == nil || strings.Contains(err.Error(), testTelegramToken) {
		t.Fatal(err)
	}
}

// Publishing from the console reaches Telegram and the unauthenticated public feed, and a Telegram failure leaves nothing published.
func TestPublicNoticesFeedAndTelegramPublish(t *testing.T) {
	disposableSchema(t)
	t.Setenv("TEST_AUTH_PEPPER", strings.Repeat("p", 64))
	t.Setenv("TEST_CLIENT_TOKEN", testToken)
	adminToken := strings.Repeat("q", 48)
	t.Setenv("TEST_ADMIN_TOKEN", adminToken)
	s, err := New(Config{
		Auth:    account.Config{Enabled: true, DatabaseEnv: "MSIME_TEST_DATABASE_URL", PepperEnv: "TEST_AUTH_PEPPER"},
		Admin:   AdminConfig{Enabled: true, Host: "admin.example.com", TokenEnv: "TEST_ADMIN_TOKEN"},
		Clients: []Client{{ID: "device", TokenEnv: "TEST_CLIENT_TOKEN", RequestsPerMinute: 120}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer s.CloseAccounts()
	fake := &fakeTelegram{status: 200, ok: true}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s.config.Admin.Telegram = AdminTelegramConfig{ChatID: "-1001234567890", APIURL: srv.URL, botToken: testTelegramToken}
	s.accounts.ConfigureNoticeBroadcaster(s.noticeBroadcaster())

	action := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://admin.example.com/api/actions", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+adminToken)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://admin.example.com")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	feed := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", noticesPath+query, nil))
		return w
	}

	if w := action(`{"action":"publish_notice","value":{"title":"iOS 1.0.0 公开测试开始","body":"欢迎参与","targets":["ios"],"channels":["site","app","telegram"]}}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(fake.messages) != 1 || fake.messages[0]["chat_id"] != "-1001234567890" {
		t.Fatal(fake.messages)
	}

	fake.status = 500
	if w := action(`{"action":"publish_notice","value":{"title":"失败的公告","targets":["all"],"channels":["telegram"]}}`); w.Code != 502 || !strings.Contains(w.Body.String(), "telegram_failed") {
		t.Fatal(w.Code, w.Body.String())
	}

	w := feed("?platform=ios&channel=app")
	var body struct {
		Items []struct {
			Title       string `json:"title"`
			PublishedAt string `json:"published_at"`
		} `json:"items"`
	}
	if w.Code != 200 || w.Header().Get("Cache-Control") != "public, max-age=60" || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Items) != 1 || body.Items[0].Title != "iOS 1.0.0 公开测试开始" || body.Items[0].PublishedAt == "" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	if w = feed("?platform=windows"); w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = feed("?platform=symbian"); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_platform") {
		t.Fatal(w.Code, w.Body.String())
	}
	r := httptest.NewRequest("GET", noticesPath, nil)
	r.Header.Set("Origin", "https://untrusted.example")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin feed request accepted", w.Code)
	}
}
