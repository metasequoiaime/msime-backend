package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/metasequoiaime/MSIME-Backend/internal/account"
)

// Telegram notice channel (unit U8).

// telegramMessageMax is Telegram's limit for one sendMessage text, in characters.
const telegramMessageMax = 4096

// telegramNotices posts published notices to one chat with the Bot API.
type telegramNotices struct {
	endpoint string
	chatID   string
	client   *http.Client
}

// noticeBroadcaster returns what delivers published notices to admin.telegram, or nil when no channel is configured.
func (s *Server) noticeBroadcaster() account.NoticeBroadcaster {
	t := s.config.Admin.Telegram
	if t.ChatID == "" || t.botToken == "" {
		return nil
	}
	return &telegramNotices{
		endpoint: strings.TrimRight(t.APIURL, "/") + "/bot" + t.botToken + "/sendMessage",
		chatID:   t.ChatID,
		client:   &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

// telegramText is the plain-text message: the title, a blank line and the body, cut to Telegram's limit on a character boundary.
func telegramText(n account.Notice) string {
	text := n.Title
	if n.Body != "" {
		text += "\n\n" + n.Body
	}
	if utf8.RuneCountInString(text) <= telegramMessageMax {
		return text
	}
	runes := []rune(text)
	return string(runes[:telegramMessageMax-1]) + "…"
}

// BroadcastNotice sends n and succeeds only when the Bot API confirms delivery. Errors never include the request URL, because it carries the bot token.
func (t *telegramNotices) BroadcastNotice(ctx context.Context, n account.Notice) error {
	body, err := json.Marshal(map[string]any{"chat_id": t.chatID, "text": telegramText(n), "disable_web_page_preview": true})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("telegram: invalid endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return errors.New("telegram: request failed")
	}
	defer resp.Body.Close()
	var reply struct {
		OK bool `json:"ok"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&reply) != nil || resp.StatusCode != http.StatusOK || !reply.OK {
		return fmt.Errorf("telegram: sendMessage answered %d", resp.StatusCode)
	}
	return nil
}
