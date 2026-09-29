package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigurationBoundaries(t *testing.T) {
	t.Setenv("CONFIG_TEST_TOKEN", strings.Repeat("a", 32))
	t.Setenv("CONFIG_TEST_BAD", "bad\ncredential")
	base := func() Config {
		return Config{Clients: []Client{{ID: "local", TokenEnv: "CONFIG_TEST_TOKEN", RequestsPerMinute: 1}}}
	}
	cases := map[string]func(*Config){
		"models limit":         func(c *Config) { c.Chat.Models = make([]string, 33) },
		"duplicate model":      func(c *Config) { c.Chat.Models = []string{"a", "a"} },
		"relative skins":       func(c *Config) { c.SkinsRoot = "relative" },
		"relative engine":      func(c *Config) { c.Engine.Binary = "relative" },
		"concurrency low":      func(c *Config) { c.MaxConcurrent = -1 },
		"concurrency high":     func(c *Config) { c.MaxConcurrent = 1025 },
		"timeout low":          func(c *Config) { c.TimeoutSeconds = -1 },
		"timeout high":         func(c *Config) { c.TimeoutSeconds = 121 },
		"no clients":           func(c *Config) { c.Clients = nil },
		"duplicate client":     func(c *Config) { c.Clients = append(c.Clients, c.Clients[0]) },
		"missing client token": func(c *Config) { c.Clients[0].TokenEnv = "CONFIG_TEST_ABSENT" },
		"rate low":             func(c *Config) { c.Clients[0].RequestsPerMinute = 0 },
		"rate high":            func(c *Config) { c.Clients[0].RequestsPerMinute = 100001 },
		"translation provider": func(c *Config) { c.Translation.Provider = "unknown" },
		"niutrans credentials": func(c *Config) { c.Translation.Provider = "niutrans" },
		"niutrans path": func(c *Config) {
			c.Translation = TranslationEndpoint{Provider: "niutrans", AppIDEnv: "CONFIG_TEST_TOKEN", APIKeyEnv: "CONFIG_TEST_TOKEN", Endpoint: Endpoint{URL: "https://example.com/other"}}
		},
		"niutrans query": func(c *Config) {
			c.Translation = TranslationEndpoint{Provider: "niutrans", AppIDEnv: "CONFIG_TEST_TOKEN", APIKeyEnv: "CONFIG_TEST_TOKEN", Endpoint: Endpoint{URL: "https://example.com/v2/text/translate?apikey=secret"}}
		},
		"tencent path":        func(c *Config) { c.Translation.Provider = "tencent"; c.Translation.URL = "https://example.com/path" },
		"tencent credentials": func(c *Config) { c.Translation.Provider = "tencent" },
		"tencent region": func(c *Config) {
			c.Translation = TranslationEndpoint{Provider: "tencent", SecretIDEnv: "CONFIG_TEST_TOKEN", Region: "invalid/region", Endpoint: Endpoint{TokenEnv: "CONFIG_TEST_TOKEN"}}
		},
		"endpoint http":        func(c *Config) { c.Cloud.URL = "http://example.com" },
		"endpoint credentials": func(c *Config) { c.Cloud.URL = "https://user@example.com" },
		"endpoint fragment":    func(c *Config) { c.Cloud.URL = "https://example.com/#x" },
		"endpoint token":       func(c *Config) { c.Cloud = Endpoint{URL: "https://example.com", TokenEnv: "CONFIG_TEST_BAD"} },
		"endpoint model":       func(c *Config) { c.Chat.URL = "https://example.com" },
		"stream provider":      func(c *Config) { c.Streaming.Provider = "unknown" },
		"stream seconds low":   func(c *Config) { c.Streaming.MaxSeconds = -1 },
		"stream seconds high":  func(c *Config) { c.Streaming.MaxSeconds = 601 },
		"stream url":           func(c *Config) { c.Streaming.URL = "wss://example.com/?x=1" },
		"stream token":         func(c *Config) { c.Streaming.URL = "wss://example.com" },
		"stream model": func(c *Config) {
			c.Streaming = StreamingEndpoint{Provider: "everyapi", URL: "wss://example.com", TokenEnv: "CONFIG_TEST_TOKEN"}
		},
		"stream resource": func(c *Config) {
			c.Streaming = StreamingEndpoint{Provider: "doubao", URL: "wss://example.com", TokenEnv: "CONFIG_TEST_TOKEN"}
		},
		"origin": func(c *Config) { c.AllowedOrigins = []string{"https://example.com/path"} },
		"auth":   func(c *Config) { c.Auth.Enabled = true },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := base()
			mutate(&c)
			if c.Validate() == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	c := base()
	c.Translation = TranslationEndpoint{Provider: "tencent", SecretIDEnv: "CONFIG_TEST_TOKEN", Endpoint: Endpoint{TokenEnv: "CONFIG_TEST_TOKEN"}}
	c.Streaming = StreamingEndpoint{Provider: "doubao", URL: "wss://example.com", TokenEnv: "CONFIG_TEST_TOKEN", AppKeyEnv: "CONFIG_TEST_TOKEN", ResourceID: "resource"}
	c.AllowedOrigins = []string{"https://example.com"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_NIUTRANS_APP", "synthetic-app")
	t.Setenv("CONFIG_NIUTRANS_KEY", "synthetic-key")
	niu := base()
	niu.Translation = TranslationEndpoint{Provider: "niutrans", AppIDEnv: "CONFIG_NIUTRANS_APP", APIKeyEnv: "CONFIG_NIUTRANS_KEY"}
	if err := niu.Validate(); err != nil {
		t.Fatal(err)
	}
	if niu.Translation.URL != "https://api.niutrans.com/v2/text/translate" || niu.Translation.appID != "synthetic-app" || niu.Translation.apiKey != "synthetic-key" {
		t.Fatalf("NiuTrans defaults or credentials not loaded: %+v", niu.Translation)
	}
	if c.Listen != "127.0.0.1:8080" || c.MaxConcurrent != 32 || c.TimeoutSeconds != 30 || c.Translation.Region != "ap-guangzhou" || c.Translation.URL != "https://tmt.tencentcloudapi.com/" {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestLoadConfigStrictJSON(t *testing.T) {
	t.Setenv("CONFIG_TEST_TOKEN", strings.Repeat("a", 32))
	valid := `{"clients":[{"id":"local","token_env":"CONFIG_TEST_TOKEN","requests_per_minute":1}]}`
	path := filepath.Join(t.TempDir(), "config.json")
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("missing file accepted")
	}
	for _, body := range []string{`{`, `{"unknown":true}`, valid + ` {}`, `{}`} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Clients) != 1 || c.Clients[0].token != os.Getenv("CONFIG_TEST_TOKEN") {
		t.Fatal("credentials not loaded")
	}
}
