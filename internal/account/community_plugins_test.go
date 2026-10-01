package account

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const pluginSoundManifest = `schema_version = 1
kind = "sound"
id = "community-clicks"
name = "Clicks"
version = "1.0.0"
license = "CC-BY-4.0"
author = "Tester"
permissions = []

[sounds]
default = "click.wav"
enter = "enter.wav"
`

const pluginMusicManifest = `schema_version = 1
kind = "music"
id = "community-rain"
name = "Rain"
version = "2.1"
license = "CC0-1.0"

[music]
tracks = ["rain.ogg", "night.wav"]
`

const pluginCommandManifest = `schema_version = 1
kind = "command_table"
id = "community-dates"
name = "Dates"
version = "1"
license = "MIT"

[[commands]]
trigger = "today"
title = "Today"
template = "Today is {date:%Y-%m-%d}, {weekday}"

[[commands]]
trigger = "now"
title = "Now"
template = "{time}"
`

const pluginEffectManifest = `schema_version = 1
kind = "effect"
id = "community-sparks"
name = "Sparks"
version = "1"
license = "MIT"

[effect]
style = "sparks"
intensity = 60
`

var (
	pluginWAV = "RIFF\x24\x00\x00\x00WAVEfmt " + strings.Repeat("\x00", 64)
	pluginOGG = "OggS" + strings.Repeat("\x00", 64)
)

// pluginFile is one zip member for the test archives; mode and flags are optional.
type pluginFile struct {
	name, data string
	mode       fs.FileMode
	flags      uint16
}

func pluginZip(t *testing.T, files ...pluginFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Deflate, Flags: f.flags}
		if f.mode != 0 {
			h.SetMode(f.mode)
		}
		out, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = out.Write([]byte(f.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func pluginSoundZip(t *testing.T) []byte {
	return pluginZip(t, pluginFile{name: "plugin.toml", data: pluginSoundManifest}, pluginFile{name: "click.wav", data: pluginWAV}, pluginFile{name: "enter.wav", data: pluginWAV}, pluginFile{name: "LICENSE.txt", data: "CC-BY-4.0"})
}

func TestPluginArchiveAcceptsEveryKind(t *testing.T) {
	for _, tc := range []struct {
		kind, id, version string
		archive           []byte
	}{
		{"sound", "community-clicks", "1.0.0", pluginSoundZip(t)},
		{"music", "community-rain", "2.1", pluginZip(t, pluginFile{name: "rain/plugin.toml", data: pluginMusicManifest}, pluginFile{name: "rain/rain.ogg", data: pluginOGG}, pluginFile{name: "rain/night.wav", data: pluginWAV}, pluginFile{name: "rain/", mode: fs.ModeDir | 0o755})},
		{"command_table", "community-dates", "1", pluginZip(t, pluginFile{name: "plugin.toml", data: pluginCommandManifest}, pluginFile{name: "README.md", data: "# Dates"})},
		{"command_table", "community-dates", "1", pluginZip(t, pluginFile{name: "plugin.toml", data: pluginCommandManifest}, pluginFile{name: ".DS_Store", data: "x"}, pluginFile{name: "__MACOSX/._plugin.toml", data: "resource fork"})},
	} {
		pack, code := validPluginArchive(tc.archive)
		if code != "" || pack.Kind != tc.kind || pack.ID != tc.id || pack.Version != tc.version || pack.License == "" || len(pack.Manifest) == 0 {
			t.Fatal(tc.kind, code, pack)
		}
	}
}

func TestPluginArchiveRejectsUnsafeArchives(t *testing.T) {
	manifest := pluginFile{name: "plugin.toml", data: pluginSoundManifest}
	audio := []pluginFile{{name: "click.wav", data: pluginWAV}, {name: "enter.wav", data: pluginWAV}}
	with := func(extra ...pluginFile) []byte {
		return pluginZip(t, append(append([]pluginFile{manifest}, audio...), extra...)...)
	}
	many := func(n int, prefix string) []byte {
		files := []pluginFile{manifest}
		files = append(files, audio...)
		for i := 0; len(files) < n; i++ {
			files = append(files, pluginFile{name: fmt.Sprintf("%snote%d.txt", prefix, i), data: "x"})
		}
		return pluginZip(t, files...)
	}
	// A stored member whose declared size is smaller than its bytes, so only a bounded reader notices.
	var forged bytes.Buffer
	fw := zip.NewWriter(&forged)
	for _, f := range []pluginFile{manifest, audio[0], audio[1]} {
		out, _ := fw.Create(f.name)
		out.Write([]byte(f.data))
	}
	body := []byte("a notice that is longer than it claims")
	raw, err := fw.CreateRaw(&zip.FileHeader{Name: "NOTICE.txt", Method: zip.Store, CRC32: crc32.ChecksumIEEE(body), CompressedSize64: uint64(len(body)), UncompressedSize64: 4})
	if err != nil {
		t.Fatal(err)
	}
	raw.Write(body)
	fw.Close()
	for _, tc := range []struct {
		name    string
		archive []byte
		code    string
	}{
		{"not a zip", []byte("plugin.toml"), "invalid_plugin_archive"},
		{"empty", nil, "plugin_too_large"},
		{"over 8 MiB", make([]byte, maxPluginArchiveBytes+1), "plugin_too_large"},
		{"parent traversal", with(pluginFile{name: "../escape.txt", data: "x"}), "invalid_plugin_archive"},
		{"nested traversal", with(pluginFile{name: "pack/../../escape.txt", data: "x"}), "invalid_plugin_archive"},
		{"absolute", with(pluginFile{name: "/etc/passwd.txt", data: "x"}), "invalid_plugin_archive"},
		{"backslash", with(pluginFile{name: "..\\escape.txt", data: "x"}), "invalid_plugin_archive"},
		{"drive letter", with(pluginFile{name: "C:escape.txt", data: "x"}), "invalid_plugin_archive"},
		{"symlink", with(pluginFile{name: "link.txt", data: "/etc/passwd", mode: fs.ModeSymlink | 0o777}), "invalid_plugin_archive"},
		{"hidden symlink", with(pluginFile{name: ".link", data: "/etc/passwd", mode: fs.ModeSymlink | 0o777}), "invalid_plugin_archive"},
		{"encrypted", with(pluginFile{name: "secret.txt", data: "x", flags: 0x1}), "invalid_plugin_archive"},
		{"nested zip by name", with(pluginFile{name: "inner.zip", data: "x"}), "invalid_plugin_archive"},
		{"nested zip by name hidden", with(pluginFile{name: "__MACOSX/inner.tar.gz", data: "x"}), "invalid_plugin_archive"},
		{"nested zip by magic", with(pluginFile{name: "notes.txt", data: "PK\x03\x04rest"}), "invalid_plugin_archive"},
		{"nested gzip by magic", with(pluginFile{name: "notes.md", data: "\x1f\x8b\x08rest"}), "invalid_plugin_archive"},
		{"deep folder", with(pluginFile{name: "a/b/c.txt", data: "x"}), "invalid_plugin_archive"},
		{"deep directory", with(pluginFile{name: "a/b/", mode: fs.ModeDir | 0o755}), "invalid_plugin_archive"},
		{"two wrappers", pluginZip(t, pluginFile{name: "a/plugin.toml", data: pluginSoundManifest}, pluginFile{name: "b/click.wav", data: pluginWAV}), "invalid_plugin_archive"},
		{"case duplicate", with(pluginFile{name: "Click.wav", data: pluginWAV}), "invalid_plugin_archive"},
		{"bad file name", with(pluginFile{name: "注意.txt", data: "x"}), "invalid_plugin_archive"},
		{"forged size", forged.Bytes(), "invalid_plugin_archive"},
		{"65 members", many(65, "."), "plugin_too_large"},
		{"17 files", many(17, ""), "plugin_too_large"},
		{"inflation ratio", with(pluginFile{name: "bomb.txt", data: strings.Repeat("\x00", 4<<20)}), "plugin_too_large"},
		{"member over 16 MiB", with(pluginFile{name: "bomb.txt", data: strings.Repeat("\x00", maxPluginFileBytes+1)}), "plugin_too_large"},
		{"oversized notice", with(pluginFile{name: "notice.txt", data: strings.Repeat("a", maxPluginNoticeBytes+1)}), "plugin_too_large"},
	} {
		if _, code := validPluginArchive(tc.archive); code != tc.code {
			t.Errorf("%s: got %q, want %q", tc.name, code, tc.code)
		}
	}
}

func TestPluginArchiveRejectsInvalidManifests(t *testing.T) {
	sound := func(manifest string, files ...pluginFile) []byte {
		if files == nil {
			files = []pluginFile{{name: "click.wav", data: pluginWAV}, {name: "enter.wav", data: pluginWAV}}
		}
		return pluginZip(t, append([]pluginFile{{name: "plugin.toml", data: manifest}}, files...)...)
	}
	replace := func(old, new string) []byte {
		if !strings.Contains(pluginSoundManifest, old) {
			t.Fatal("fixture lacks", old)
		}
		return sound(strings.Replace(pluginSoundManifest, old, new, 1))
	}
	command := func(old, new string) []byte {
		return pluginZip(t, pluginFile{name: "plugin.toml", data: strings.Replace(pluginCommandManifest, old, new, 1)})
	}
	for _, tc := range []struct {
		name    string
		archive []byte
		code    string
	}{
		{"no manifest", pluginZip(t, pluginFile{name: "click.wav", data: pluginWAV}), "invalid_plugin_manifest"},
		{"not toml", sound("kind = "), "invalid_plugin_manifest"},
		{"invalid utf-8", replace(`name = "Clicks"`, "name = \"\xff\""), "invalid_plugin_manifest"},
		{"schema version 2", replace("schema_version = 1", "schema_version = 2"), "invalid_plugin_manifest"},
		{"schema version float", replace("schema_version = 1", "schema_version = 1.0"), "invalid_plugin_manifest"},
		{"missing kind", replace(`kind = "sound"`, ""), "invalid_plugin_manifest"},
		{"unknown kind", replace(`kind = "sound"`, `kind = "theme"`), "invalid_plugin_manifest"},
		{"upper-case key", replace(`kind = "sound"`, `Kind = "sound"`), "invalid_plugin_manifest"},
		{"unknown key", replace(`author = "Tester"`, `homepage = "https://example.test"`), "invalid_plugin_manifest"},
		{"other kind's table", replace("[sounds]", "[music]\ntracks = [\"click.wav\"]\n[sounds]"), "invalid_plugin_manifest"},
		{"unknown nested key", replace(`enter = "enter.wav"`, `enter = "enter.wav"`+"\nBackspace = \"click.wav\""), "invalid_plugin_manifest"},
		{"duplicate key", replace(`author = "Tester"`, `author = "Tester"`+"\nauthor = \"Again\""), "invalid_plugin_manifest"},
		{"permissions", replace("permissions = []", `permissions = ["network"]`), "invalid_plugin_manifest"},
		{"permissions string", replace("permissions = []", `permissions = "network"`), "invalid_plugin_manifest"},
		{"bad id", replace(`id = "community-clicks"`, `id = "Community"`), "invalid_plugin_manifest"},
		{"traversing id", replace(`id = "community-clicks"`, `id = ".."`), "invalid_plugin_manifest"},
		{"built-in id", replace(`id = "community-clicks"`, `id = "twinkle"`), "invalid_plugin_manifest"},
		{"blank name", replace(`name = "Clicks"`, `name = "  "`), "invalid_plugin_manifest"},
		{"control in name", replace(`name = "Clicks"`, `name = "a\tb"`), "invalid_plugin_manifest"},
		{"long version", replace(`version = "1.0.0"`, `version = "`+strings.Repeat("1", 33)+`"`), "invalid_plugin_manifest"},
		{"license characters", replace(`license = "CC-BY-4.0"`, `license = "MIT; rm -rf"`), "invalid_plugin_manifest"},
		{"bad audio magic", sound(pluginSoundManifest, pluginFile{name: "click.wav", data: "hello"}, pluginFile{name: "enter.wav", data: pluginWAV}), "invalid_plugin_manifest"},
		{"ogg named wav", sound(pluginSoundManifest, pluginFile{name: "click.wav", data: pluginOGG}, pluginFile{name: "enter.wav", data: pluginWAV}), "invalid_plugin_manifest"},
		{"empty audio", sound(pluginSoundManifest, pluginFile{name: "click.wav", data: ""}, pluginFile{name: "enter.wav", data: pluginWAV}), "invalid_plugin_manifest"},
		{"missing audio", sound(pluginSoundManifest, pluginFile{name: "click.wav", data: pluginWAV}), "invalid_plugin_manifest"},
		{"unreferenced audio", sound(pluginSoundManifest, pluginFile{name: "click.wav", data: pluginWAV}, pluginFile{name: "enter.wav", data: pluginWAV}, pluginFile{name: "extra.wav", data: pluginWAV}), "invalid_plugin_manifest"},
		{"unsupported audio", replace(`default = "click.wav"`, `default = "click.mp3"`), "invalid_plugin_manifest"},
		{"ogg key sample", sound(strings.Replace(pluginSoundManifest, `enter = "enter.wav"`, `enter = "enter.ogg"`, 1), pluginFile{name: "click.wav", data: pluginWAV}, pluginFile{name: "enter.ogg", data: pluginOGG}), "invalid_plugin_manifest"},
		{"ogg sequence sample", sound(strings.Replace(pluginSoundManifest, "[sounds]\ndefault = \"click.wav\"\nenter = \"enter.wav\"\n", "mode = \"sequence\"\n[sequence]\nsample = \"tone.ogg\"\nsemitones = [0]\n", 1), pluginFile{name: "tone.ogg", data: pluginOGG}), "invalid_plugin_manifest"},
		{"oversized sample", sound(pluginSoundManifest, pluginFile{name: "click.wav", data: pluginWAV + strings.Repeat("\x01", maxSoundSampleBytes)}, pluginFile{name: "enter.wav", data: pluginWAV}), "plugin_too_large"},
		{"keys mode without default", replace(`default = "click.wav"`, `space = "click.wav"`), "invalid_plugin_manifest"},
		{"sequence semitone range", replace("[sounds]", "mode = \"sequence\"\n[sequence]\nsample = \"click.wav\"\nsemitones = [0, 25]\n[sounds]"), "invalid_plugin_manifest"},
		{"bad trigger", command(`trigger = "now"`, `trigger = "Now"`), "invalid_plugin_manifest"},
		{"duplicate trigger", command(`trigger = "now"`, `trigger = "today"`), "invalid_plugin_manifest"},
		{"unknown placeholder", command(`template = "{time}"`, `template = "{foo}"`), "invalid_plugin_manifest"},
		{"stray brace", command(`template = "{time}"`, `template = "{time"`), "invalid_plugin_manifest"},
		{"long template", command(`template = "{time}"`, `template = "`+strings.Repeat("字", 200)+`"`), "invalid_plugin_manifest"},
		{"unknown strftime specifier", command(`template = "{time}"`, `template = "{date:%Q}"`), "invalid_plugin_manifest"},
		{"strftime newline", command(`template = "{time}"`, `template = "{time:%n}"`), "invalid_plugin_manifest"},
		{"strftime tab", command(`template = "{time}"`, `template = "{date:%-t}"`), "invalid_plugin_manifest"},
		{"strftime offset", command(`template = "{time}"`, `template = "{time:%H%z}"`), "invalid_plugin_manifest"},
		{"strftime timestamp", command(`template = "{time}"`, `template = "{time:%s}"`), "invalid_plugin_manifest"},
		{"strftime trailing percent", command(`template = "{time}"`, `template = "{date:%Y%}"`), "invalid_plugin_manifest"},
		{"long expansion", command(`template = "{time}"`, `template = "`+strings.Repeat("a", 178)+`{date:%A %B %d}"`), "invalid_plugin_manifest"},
		// The client has no effect kind: its effects are built into the hosts and tuned only through preferences.
		{"effect kind", pluginZip(t, pluginFile{name: "plugin.toml", data: pluginEffectManifest}), "invalid_plugin_manifest"},
	} {
		if _, code := validPluginArchive(tc.archive); code != tc.code {
			t.Errorf("%s: got %q, want %q", tc.name, code, tc.code)
		}
	}
	// A valid sequence-mode pack, so the semitone case above fails only on its range.
	sequence := strings.Replace(pluginSoundManifest, "[sounds]\ndefault = \"click.wav\"\nenter = \"enter.wav\"\n", "mode = \"sequence\"\n[sequence]\nsample = \"click.wav\"\nsemitones = [0, 2, 4, -24, 24]\nadvance = \"commit\"\n", 1)
	if _, code := validPluginArchive(sound(sequence, pluginFile{name: "click.wav", data: pluginWAV})); code != "" {
		t.Fatal("sequence", code)
	}
	// The longest expansion that still fits: "Wednesday September 30" brings 177 letters to 199 UTF-16 units.
	if _, code := validPluginArchive(command(`template = "{time}"`, `template = "`+strings.Repeat("a", 177)+`{date:%A %B %d}"`)); code != "" {
		t.Fatal("longest expansion", code)
	}
}

func TestCommandTemplateExpansion(t *testing.T) {
	september, december := commandTemplateInstants[0], commandTemplateInstants[1]
	for _, tc := range []struct {
		template string
		at       int
		want     string
	}{
		{"{date} {time} {weekday}", 0, "2026-09-30 23:59 星期三"},
		{"{date:}{time:}", 1, "2026-12-3023:59"},
		{"{date:%-m/%_m/%m/%0e/%e}", 0, "9/ 9/09/30/30"},
		{"{date:%-m}", 1, "12"},
		{"{date:%a %A %b %h %B %C %y %Y %G %g}", 0, "Wed Wednesday Sep Sep September 20 26 2026 2026 26"},
		{"{date:%j %-j %U %W %V %u %w}", 0, "273 273 39 39 40 3 4"},
		{"{date:%j %U %W %V}", 1, "364 52 52 53"},
		{"{date:%c|%D|%x|%F}", 0, "Wed Sep 30 23:59:59 2026|09/30/26|09/30/26|2026-09-30"},
		{"{time:%H %I %k %l %M %S %p %P}", 0, "23 11 23 11 59 59 PM pm"},
		{"{time:%r|%R|%T|%X|%%|%-%}", 0, "11:59:59 PM|23:59|23:59:59|23:59:59|%|%"},
		{"{time:%n%t}", 0, "\n\t"},
		{"签名 {date:%Y年%-m月%-d日}", 0, "签名 2026年9月30日"},
	} {
		at := september
		if tc.at == 1 {
			at = december
		}
		if got, ok := expandCommandTemplate(tc.template, at); !ok || got != tc.want {
			t.Errorf("%s: got %q %v, want %q", tc.template, got, ok, tc.want)
		}
	}
	for _, template := range []string{"{date:%Q}", "{date:%E}", "{date:%Oy}", "{time:%Z}", "{time:%z}", "{time:%s}", "{date:%}", "{date:%-}", "{date:%_é}", "{weekday:%A}", "{now}", "{}", "}", "{date"} {
		if got, ok := expandCommandTemplate(template, september); ok {
			t.Errorf("%s: accepted as %q", template, got)
		}
	}
}

type pluginClient struct {
	t *testing.T
	a *Service
}

func (c pluginClient) do(method, path, body, token string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	Mount(mux, c.a)
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func pluginPublishBody(id, name, kind, pluginID, version string, archive []byte) string {
	body, _ := json.Marshal(map[string]any{"id": id, "name": name, "description": "社区插件", "kind": kind, "plugin_id": pluginID, "version": version, "archive": archive})
	return string(body)
}

func TestCommunityPluginLifecycle(t *testing.T) {
	store := testStore(t)
	owner := complete(t, store, Identity{"apple", "plugin-owner"})
	user := complete(t, store, Identity{"apple", "plugin-reader"})
	c := pluginClient{t, &Service{store: store}}
	archive := pluginSoundZip(t)
	digest := sha256.Sum256(archive)
	id := "ab334455-1234-1234-1234-1234567890ab"
	path := "/v1/community/plugins/" + id
	body := pluginPublishBody(id, "按键音", "sound", "community-clicks", "1.0.0", archive)
	if w := c.do("POST", "/v1/community/plugins", body, ""); w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := c.do("POST", "/v1/community/plugins", body, owner.AccessToken)
	var item CommunityPlugin
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &item) != nil || item.SHA256 != hex.EncodeToString(digest[:]) || item.Size != int64(len(archive)) || !item.Owned || item.License != "CC-BY-4.0" || item.PluginID != "community-clicks" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := c.do("POST", "/v1/community/plugins", strings.Replace(body, id, strings.ToUpper(id), 1), owner.AccessToken); w.Code != 200 {
		t.Fatal("retry", w.Code, w.Body.String())
	}
	if w := c.do("POST", "/v1/community/plugins", strings.Replace(body, "按键音", "改名", 1), owner.AccessToken); w.Code != 409 || !strings.Contains(w.Body.String(), "plugin_id_conflict") {
		t.Fatal("changed retry", w.Code, w.Body.String())
	}
	if w := c.do("POST", "/v1/community/plugins", body, user.AccessToken); w.Code != 409 {
		t.Fatal("other account", w.Code)
	}
	commands := pluginZip(t, pluginFile{name: "plugin.toml", data: pluginCommandManifest})
	if w := c.do("POST", "/v1/community/plugins", pluginPublishBody("ab334455-1234-1234-1234-1234567890ac", "日期命令", "command_table", "community-dates", "1", commands), owner.AccessToken); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	list := func(query string) (items []CommunityPlugin, more bool) {
		w := c.do("GET", "/v1/community/plugins"+query, "", "")
		var v struct {
			Plugins []CommunityPlugin `json:"plugins"`
			HasMore *bool             `json:"has_more"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.HasMore == nil {
			t.Fatal(query, w.Code, w.Body.String())
		}
		return v.Plugins, *v.HasMore
	}
	if items, more := list(""); len(items) != 2 || more || items[0].Kind != "command_table" || strings.Contains(fmt.Sprint(items), owner.User.ID) {
		t.Fatal(items, more)
	}
	if items, _ := list("?kind=sound"); len(items) != 1 || items[0].ID != id {
		t.Fatal(items)
	}
	if items, _ := list("?q=%E6%97%A5%E6%9C%9F"); len(items) != 1 || items[0].Kind != "command_table" {
		t.Fatal(items)
	}
	if items, _ := list("?kind=music"); len(items) != 0 {
		t.Fatal(items)
	}
	for _, query := range []string{"?kind=theme", "?offset=-1", "?offset=x", "?q=" + strings.Repeat("a", 129)} {
		if w := c.do("GET", "/v1/community/plugins"+query, "", ""); w.Code != 400 {
			t.Fatal(query, w.Code)
		}
	}
	if w := c.do("PUT", path+"/rating", `{"stars":5}`, user.AccessToken); w.Code != 403 || !strings.Contains(w.Body.String(), "download_before_rating_or_own_plugin") {
		t.Fatal("rating before download", w.Code, w.Body.String())
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := c.do("POST", path+"/download", `{}`, user.AccessToken)
			var v struct {
				Archive []byte `json:"archive"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || !bytes.Equal(v.Archive, archive) {
				t.Error(w.Code, len(w.Body.String()))
			}
		}()
	}
	wg.Wait()
	w = c.do("POST", path+"/download", `{}`, user.AccessToken)
	var download map[string]any
	if w.Code != 200 || w.Header().Get("Content-Length") != fmt.Sprint(w.Body.Len()) || w.Header().Get("Content-Type") != "application/json; charset=utf-8" || json.Unmarshal(w.Body.Bytes(), &download) != nil || len(download) != 7 || download["id"] != id || download["kind"] != "sound" || download["sha256"] != hex.EncodeToString(digest[:]) || download["plugin_id"] != "community-clicks" || download["version"] != "1.0.0" || download["size"] != float64(len(archive)) || download["archive"] != base64.StdEncoding.EncodeToString(archive) {
		t.Fatal(w.Code, w.Body.Len())
	}
	if w := c.do("POST", path+"/download", `{}`, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := c.do("POST", "/v1/community/plugins/ab334455-0000-1234-1234-1234567890ab/download", `{}`, user.AccessToken); w.Code != 404 {
		t.Fatal(w.Code)
	}
	for _, stars := range []string{"5", "3"} {
		if w := c.do("PUT", path+"/rating", `{"stars":`+stars+`}`, user.AccessToken); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, stars := range []string{"0", "6"} {
		if w := c.do("PUT", path+"/rating", `{"stars":`+stars+`}`, user.AccessToken); w.Code != 400 {
			t.Fatal(stars, w.Code)
		}
	}
	// Downloading one's own pack counts once but never unlocks rating it.
	if w := c.do("POST", path+"/download", `{}`, owner.AccessToken); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := c.do("PUT", path+"/rating", `{"stars":5}`, owner.AccessToken); w.Code != 403 {
		t.Fatal("own rating", w.Code)
	}
	if w := c.do("PUT", "/v1/community/plugins/ab334455-0000-1234-1234-1234567890ab/rating", `{"stars":5}`, user.AccessToken); w.Code != 404 {
		t.Fatal(w.Code)
	}
	w = c.do("GET", path, "", user.AccessToken)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &item) != nil || item.Owned || item.MyRating != 3 || item.Downloads != 2 || item.RatingCount != 1 || item.RatingAverage != 3 || strings.Contains(w.Body.String(), `"archive"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = c.do("GET", path, "", owner.AccessToken)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &item) != nil || !item.Owned || item.MyRating != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := c.do("GET", "/v1/community/plugins/missing", "", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := c.do("DELETE", path, "", user.AccessToken); w.Code != 404 {
		t.Fatal("owner check", w.Code)
	}
	if w := c.do("DELETE", path, "", owner.AccessToken); w.Code != 200 || !strings.Contains(w.Body.String(), `"deleted":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, check := range []struct{ method, path, body, token string }{{"GET", path, "", ""}, {"POST", path + "/download", "{}", user.AccessToken}, {"PUT", path + "/rating", `{"stars":4}`, user.AccessToken}, {"DELETE", path, "", owner.AccessToken}} {
		if w := c.do(check.method, check.path, check.body, check.token); w.Code != 404 {
			t.Fatal(check, w.Code)
		}
	}
	var rows int
	if err := store.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM community_plugin_downloads WHERE pack_id=$1)+(SELECT count(*) FROM community_plugin_ratings WHERE pack_id=$1)`, id).Scan(&rows); err != nil || rows != 0 {
		t.Fatal(rows, err)
	}
	// Deleting the account removes its packs.
	if _, err := store.pool.Exec(context.Background(), `DELETE FROM auth_users WHERE id=$1`, owner.User.ID); err != nil {
		t.Fatal(err)
	}
	if items, _ := list(""); len(items) != 0 {
		t.Fatal(items)
	}
}

func TestCommunityPluginPublishBoundaries(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	user := complete(t, store, Identity{"apple", "plugin-bounds"})
	c := pluginClient{t, &Service{store: store}}
	archive := pluginSoundZip(t)
	publish := func(id, kind, pluginID, version string, archive []byte, token string) *httptest.ResponseRecorder {
		return c.do("POST", "/v1/community/plugins", pluginPublishBody(id, "按键音", kind, pluginID, version, archive), token)
	}
	uuid := func(n int) string { return fmt.Sprintf("ab334455-1234-1234-1234-%012d", n) }
	expect := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if w.Code != status || !strings.Contains(w.Body.String(), `"`+code+`"`) {
			t.Fatal(status, code, w.Code, w.Body.String())
		}
	}
	// Rejected before the hourly publish rate is charged.
	expect(publish("not-a-uuid", "sound", "community-clicks", "1.0.0", archive, user.AccessToken), 400, "invalid_community_id")
	expect(c.do("POST", "/v1/community/plugins", strings.Replace(pluginPublishBody(uuid(1), "x", "sound", "community-clicks", "1.0.0", archive), `"x"`, `"`+strings.Repeat("名", 33)+`"`, 1), user.AccessToken), 400, "invalid_plugin_metadata")
	expect(publish(uuid(1), "theme", "community-clicks", "1.0.0", archive, user.AccessToken), 400, "invalid_kind")
	expect(publish(uuid(1), "sound", "Community", "1.0.0", archive, user.AccessToken), 400, "invalid_plugin_metadata")
	expect(publish(uuid(1), "sound", "community-clicks", "", archive, user.AccessToken), 400, "invalid_plugin_metadata")
	expect(publish(uuid(1), "sound", "community-clicks", "1.0.0", nil, user.AccessToken), 400, "plugin_too_large")
	expect(publish(uuid(1), "sound", "community-clicks", "1.0.0", make([]byte, maxPluginArchiveBytes+1), user.AccessToken), 400, "plugin_too_large")
	expect(c.do("POST", "/v1/community/plugins", `{"id":"`+uuid(1)+`","archive":"`+strings.Repeat("A", maxPluginPublishBytes)+`"}`, user.AccessToken), 400, "invalid_json")
	expect(c.do("POST", "/v1/community/plugins", pluginPublishBody(uuid(1), "按键音", "sound", "community-clicks", "1.0.0", archive)[:40], user.AccessToken), 400, "invalid_json")
	r := httptest.NewRequest("POST", "/v1/community/plugins", strings.NewReader(pluginPublishBody(uuid(1), "按键音", "sound", "community-clicks", "1.0.0", archive)))
	r.Header.Set("Content-Type", "application/zip")
	r.Header.Set("Authorization", "Bearer "+user.AccessToken)
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	Mount(mux, c.a)
	mux.ServeHTTP(w, r)
	expect(w, 415, "json_required")
	if got := accountRouteTimeout("POST /v1/community/plugins"); got != pluginTransferTimeout {
		t.Fatal(got)
	}
	if got := accountRouteTimeout("POST /v1/community/plugins/{id}/download"); got != pluginTransferTimeout {
		t.Fatal(got)
	}
	// Rejected after the archive is read; each of these is charged against the hourly rate.
	expect(publish(uuid(1), "music", "community-clicks", "1.0.0", archive, user.AccessToken), 400, "plugin_kind_mismatch")
	expect(publish(uuid(1), "sound", "community-other", "1.0.0", archive, user.AccessToken), 400, "plugin_manifest_mismatch")
	expect(publish(uuid(1), "sound", "community-clicks", "1.0.1", archive, user.AccessToken), 400, "plugin_manifest_mismatch")
	expect(publish(uuid(1), "sound", "community-clicks", "1.0.0", []byte("not a zip"), user.AccessToken), 400, "invalid_plugin_archive")
	expect(publish(uuid(1), "sound", "community-clicks", "1.0.0", pluginZip(t, pluginFile{name: "../plugin.toml", data: pluginSoundManifest}), user.AccessToken), 400, "invalid_plugin_archive")
	expect(publish(uuid(1), "sound", "community-clicks", "1.0.0", pluginZip(t, pluginFile{name: "plugin.toml", data: pluginSoundManifest}, pluginFile{name: "bomb.txt", data: strings.Repeat("\x00", 8<<20)}), user.AccessToken), 400, "plugin_too_large")
	var count int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM community_plugins`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if w := publish(uuid(1), "sound", "community-clicks", "1.0.0", archive, user.AccessToken); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	// Seven charged attempts so far; three more reach the hourly limit of ten.
	for i := 0; i < 3; i++ {
		expect(publish(uuid(2), "music", "community-clicks", "1.0.0", archive, user.AccessToken), 400, "plugin_kind_mismatch")
	}
	expect(publish(uuid(2), "sound", "community-clicks", "1.0.0", archive, user.AccessToken), 429, "rate_limit_exceeded")
	if w := publish(uuid(1), "sound", "community-clicks", "1.0.0", archive, user.AccessToken); w.Code != 200 {
		t.Fatal("retry after limit", w.Code, w.Body.String())
	}

	// The count quota, under concurrent publishes from one account.
	quota := complete(t, store, Identity{"apple", "plugin-quota"})
	seed := func(owner string, n, size int) {
		t.Helper()
		if _, err := store.pool.Exec(ctx, `INSERT INTO community_plugins(id,owner_id,kind,plugin_id,name,version,license,manifest,archive,request_sha256)
 SELECT gen_random_uuid()::text,$1,'sound','seeded','seeded','1','MIT','x'::bytea,decode(repeat('00',$3::int),'hex'),repeat('0',64) FROM generate_series(1,$2::int)`, owner, n, size); err != nil {
			t.Fatal(err)
		}
	}
	seed(quota.User.ID, maxPluginsPerUser-1, 16)
	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = publish(uuid(100+i), "sound", "community-clicks", "1.0.0", archive, quota.AccessToken).Code
		}()
	}
	wg.Wait()
	created, limited := 0, 0
	for _, code := range codes {
		switch code {
		case 201:
			created++
		case 409:
			limited++
		}
	}
	if created != 1 || limited != 3 {
		t.Fatal(codes)
	}
	expect(publish(uuid(200), "sound", "community-clicks", "1.0.0", archive, quota.AccessToken), 409, "plugin_publish_limit")

	// The byte quota: four 8 MiB packs fill 32 MiB.
	storage := complete(t, store, Identity{"apple", "plugin-storage"})
	seed(storage.User.ID, 3, maxPluginArchiveBytes)
	seed(storage.User.ID, 1, maxPluginArchiveBytes-len(archive)+1)
	expect(publish(uuid(300), "sound", "community-clicks", "1.0.0", archive, storage.AccessToken), 409, "plugin_storage_limit")
	if _, err := store.pool.Exec(ctx, `DELETE FROM community_plugins WHERE owner_id=$1 AND size<$2`, storage.User.ID, maxPluginArchiveBytes); err != nil {
		t.Fatal(err)
	}
	if w := publish(uuid(300), "sound", "community-clicks", "1.0.0", archive, storage.AccessToken); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestCommunityPluginModeration(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, `TRUNCATE admin_events,admin_audit`); err != nil {
		t.Fatal(err)
	}
	owner := complete(t, store, Identity{"apple", "plugin-moderated"})
	user := complete(t, store, Identity{"apple", "plugin-rater"})
	a := &Service{store: store}
	c := pluginClient{t, a}
	archive := pluginSoundZip(t)
	id := "ab334455-1234-1234-1234-1234567890ad"
	if w := c.do("POST", "/v1/community/plugins", pluginPublishBody(id, "按键音", "sound", "community-clicks", "1.0.0", archive), owner.AccessToken); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := c.do("POST", "/v1/community/plugins/"+id+"/download", "{}", user.AccessToken); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := c.do("PUT", "/v1/community/plugins/"+id+"/rating", `{"stars":4}`, user.AccessToken); w.Code != 200 {
		t.Fatal(w.Code)
	}
	admin := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.AdminHTTP(w, r.WithContext(adminTestContext(r.Context(), "google:test:moderator@example.test")))
		return w
	}
	archiveText := base64.StdEncoding.EncodeToString(archive)
	w := admin("GET", "/api/plugins?q=community-clicks", "")
	var page struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Total != 1 || page.Items[0]["id"] != id || page.Items[0]["owner_id"] != owner.User.ID || page.Items[0]["downloads"] != float64(1) || strings.Contains(w.Body.String(), archiveText) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = admin("GET", "/api/plugins/"+id, "")
	var detail struct {
		Content       string  `json:"content"`
		SHA256        string  `json:"sha256"`
		Size          int     `json:"size"`
		Downloads     int     `json:"downloads"`
		RatingAverage float64 `json:"rating_average"`
	}
	digest := sha256.Sum256(archive)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail.Content != pluginSoundManifest || detail.SHA256 != hex.EncodeToString(digest[:]) || detail.Size != len(archive) || detail.Downloads != 1 || detail.RatingAverage != 4 || strings.Contains(w.Body.String(), archiveText) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := admin("GET", "/api/plugins/missing", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	w = admin("GET", "/api/overview", "")
	var overview struct {
		Plugins         int `json:"plugins"`
		PluginDownloads int `json:"plugin_downloads"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &overview) != nil || overview.Plugins != 1 || overview.PluginDownloads != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := admin("POST", "/api/actions", `{"action":"delete_plugin","id":"`+id+`"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := admin("POST", "/api/actions", `{"action":"delete_plugin","id":"`+id+`"}`); w.Code != 404 {
		t.Fatal(w.Code)
	}
	var audits int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM admin_audit WHERE action='delete_plugin' AND target=$1 AND actor='google:test:moderator@example.test'`, id).Scan(&audits); err != nil || audits != 1 {
		t.Fatal(audits, err)
	}
	if w := c.do("GET", "/v1/community/plugins/"+id, "", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestCommunityPluginTransferSlots(t *testing.T) {
	store := testStore(t)
	owner := complete(t, store, Identity{"apple", "plugin-slots-owner"})
	other := complete(t, store, Identity{"apple", "plugin-slots-other"})
	c := pluginClient{t, &Service{store: store}}
	archive := pluginSoundZip(t)
	uuid := func(n int) string { return fmt.Sprintf("ab334455-1234-1234-5678-%012d", n) }
	// waitFor sends a request whose context ends after a second, so a request that has to queue fails fast instead of waiting out the 90 s route timeout.
	waitFor := func(method, path, body, token string) *httptest.ResponseRecorder {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		mux := http.NewServeMux()
		Mount(mux, c.a)
		r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	busy := func(w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != 503 || !strings.Contains(w.Body.String(), `"plugin_busy"`) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	fill := func(slots chan struct{}) func() {
		for i := 0; i < cap(slots); i++ {
			slots <- struct{}{}
		}
		return func() {
			for i := 0; i < cap(slots); i++ {
				<-slots
			}
		}
	}

	// Every upload slot is taken, so a publish waits before its body is read: even a body that is not JSON gets 503, not 400.
	drain := fill(pluginUploadSlots)
	busy(waitFor("POST", "/v1/community/plugins", "not json", owner.AccessToken))
	drain()

	// An account's publishes take turns: while one is running, the next one from that account waits, and another account is not held up.
	release, ok := acquirePluginPublishTurn(context.Background(), owner.User.ID)
	if !ok {
		t.Fatal("turn")
	}
	busy(waitFor("POST", "/v1/community/plugins", pluginPublishBody(uuid(1), "按键音", "sound", "community-clicks", "1.0.0", archive), owner.AccessToken))
	if w := waitFor("POST", "/v1/community/plugins", pluginPublishBody(uuid(2), "按键音", "sound", "community-clicks", "1.0.0", archive), other.AccessToken); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	release()
	if w := waitFor("POST", "/v1/community/plugins", pluginPublishBody(uuid(1), "按键音", "sound", "community-clicks", "1.0.0", archive), owner.AccessToken); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	pluginPublishTurns.Lock()
	turns := len(pluginPublishTurns.accounts)
	pluginPublishTurns.Unlock()
	if turns != 0 {
		t.Fatal("turns left behind", turns)
	}

	// Every download slot is taken, so a download waits before the archive is loaded.
	path := "/v1/community/plugins/" + uuid(1) + "/download"
	drain = fill(pluginDownloadSlots)
	busy(waitFor("POST", path, "{}", other.AccessToken))
	drain()
	// That attempt was charged; the rest of the hourly allowance succeeds and the next one is refused.
	for i := 1; i < pluginDownloadsPerHour; i++ {
		if w := waitFor("POST", path, "{}", other.AccessToken); w.Code != 200 {
			t.Fatal(i, w.Code, w.Body.String())
		}
	}
	if w := waitFor("POST", path, "{}", other.AccessToken); w.Code != 429 || !strings.Contains(w.Body.String(), `"rate_limit_exceeded"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := waitFor("POST", path, "{}", owner.AccessToken); w.Code != 200 {
		t.Fatal("other account", w.Code, w.Body.String())
	}
}
