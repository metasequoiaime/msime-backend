package account

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// pluginFixtureRoot 存放与客户端解析器共用的插件包 fixture：valid/<用例>/ 下的每个目录都必须被接受，invalid/<用例>/ 下的每个目录都必须被拒绝。客户端仓库的 plugins 测试放着同样内容的一份，两边对同一批包给出相同结论，Go 校验器才算精确镜像了客户端语法。
const pluginFixtureRoot = "testdata/plugin-packs"

// pluginFixtureZip 把一个 fixture 目录按文件名排序打成 zip，文件放在 zip 根部。
func pluginFixtureZip(t *testing.T, dir string) []byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := []pluginFile{}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, pluginFile{name: entry.Name(), data: string(data)})
	}
	return pluginZip(t, files...)
}

func TestPluginFixturePacks(t *testing.T) {
	seen := map[string]int{}
	for _, group := range []string{"valid", "invalid"} {
		entries, err := os.ReadDir(filepath.Join(pluginFixtureRoot, group))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			t.Run(group+"/"+entry.Name(), func(t *testing.T) {
				pack, code := validPluginArchive(pluginFixtureZip(t, filepath.Join(pluginFixtureRoot, group, entry.Name())))
				if group == "valid" && code != "" {
					t.Fatal("rejected", code)
				}
				if group == "invalid" && code == "" {
					t.Fatal("accepted", pack.Kind, pack.ID)
				}
				if group == "valid" && !slices.Contains(pluginKinds, pack.Kind) {
					t.Fatal(pack.Kind)
				}
			})
			seen[group+"/"+strings.SplitN(entry.Name(), "-", 2)[0]]++
		}
	}
	// 每种新类型都至少有一个接受和一个拒绝的用例，fixture 目录被误删时测试不会悄悄变空。
	for _, prefix := range []string{"phrase", "helpcode", "wordbook", "symbol"} {
		if seen["valid/"+prefix] == 0 || seen["invalid/"+prefix] == 0 {
			t.Fatal("fixtures missing for", prefix, seen)
		}
	}
}

// pluginNoise 返回 n 个不可压缩的伪随机字节。大小检查发生在内容解析之前，用它测到的是数据文件自己的上限，而不是解压比例。
func pluginNoise(n int) string {
	b := make([]byte, n)
	r := rand.New(rand.NewPCG(1, 2))
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return string(b)
}

const pluginHead = "schema_version = 1\nkind = %q\nid = \"bounds\"\nname = \"Bounds\"\nversion = \"1\"\nlicense = \"MIT\"\n"

// TestPluginDataKindLimits 覆盖 fixture 里放不下的数量和大小上限：恰好在上限时接受，多一条或多一字节时拒绝。
func TestPluginDataKindLimits(t *testing.T) {
	phrases := func(n int) []byte {
		var b strings.Builder
		fmt.Fprintf(&b, pluginHead, "phrase_table")
		for i := range n {
			fmt.Fprintf(&b, "[[phrases]]\nkey = \"k\"\ntext = \"t%d\"\n", i)
		}
		return pluginZip(t, pluginFile{name: "plugin.toml", data: b.String()})
	}
	helpcode := func(table string) []byte {
		return pluginZip(t, pluginFile{name: "plugin.toml", data: fmt.Sprintf(pluginHead, "helpcode") + "[helpcode]\ntable = \"table.txt\"\n"}, pluginFile{name: "table.txt", data: table})
	}
	helpcodeEntries := func(n int) string {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, "%c=ab\n", rune(0x4e00+i))
		}
		return b.String()
	}
	wordbook := func(words string) []byte {
		return pluginZip(t, pluginFile{name: "plugin.toml", data: fmt.Sprintf(pluginHead, "wordbook") + "[wordbook]\nfile = \"words.tsv\"\n"}, pluginFile{name: "words.tsv", data: words})
	}
	wordbookEntries := func(n int) string {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, "w%d\tm\n", i)
		}
		return b.String()
	}
	for _, tc := range []struct {
		name    string
		archive []byte
		code    string
	}{
		{"2000 phrases", phrases(maxPhraseRows), ""},
		{"2001 phrases", phrases(maxPhraseRows + 1), "invalid_plugin_manifest"},
		{"30000 helpcodes", helpcode(helpcodeEntries(maxHelpcodeEntries)), ""},
		{"30001 helpcodes", helpcode(helpcodeEntries(maxHelpcodeEntries + 1)), "invalid_plugin_manifest"},
		{"helpcode table over 1 MiB", helpcode(pluginNoise(maxHelpcodeBytes + 1)), "plugin_too_large"},
		{"20000 words", wordbook(wordbookEntries(maxWordbookEntries)), ""},
		{"20001 words", wordbook(wordbookEntries(maxWordbookEntries + 1)), "invalid_plugin_manifest"},
		{"wordbook over 4 MiB", wordbook(pluginNoise(maxWordbookBytes + 1)), "plugin_too_large"},
		// 数据文件只按名字免于说明文件的 64 KiB 上限，同包里多余的大说明文件仍然拒绝。
		{"oversized notice beside data", pluginZip(t, pluginFile{name: "plugin.toml", data: fmt.Sprintf(pluginHead, "wordbook") + "[wordbook]\nfile = \"words.tsv\"\n"}, pluginFile{name: "words.tsv", data: "w\tm\n"}, pluginFile{name: "NOTICE.txt", data: strings.Repeat("a", maxPluginNoticeBytes+1)}), "plugin_too_large"},
		{"new kind with permissions", pluginZip(t, pluginFile{name: "plugin.toml", data: fmt.Sprintf(pluginHead, "symbol_set") + "permissions = [\"network\"]\n[[groups]]\ntab = \"symbols\"\ntitle = \"t\"\nitems = [\"→\"]\n"}), "invalid_plugin_manifest"},
		{"new kind with another kind's table", pluginZip(t, pluginFile{name: "plugin.toml", data: fmt.Sprintf(pluginHead, "symbol_set") + "[[phrases]]\nkey = \"k\"\ntext = \"t\"\n[[groups]]\ntab = \"symbols\"\ntitle = \"t\"\nitems = [\"→\"]\n"}), "invalid_plugin_manifest"},
		{"new kind with audio", pluginZip(t, pluginFile{name: "plugin.toml", data: fmt.Sprintf(pluginHead, "phrase_table") + "[[phrases]]\nkey = \"k\"\ntext = \"t\"\n"}, pluginFile{name: "click.wav", data: pluginWAV}), "invalid_plugin_manifest"},
	} {
		if _, code := validPluginArchive(tc.archive); code != tc.code {
			t.Errorf("%s: got %q, want %q", tc.name, code, tc.code)
		}
	}
}

// TestPluginArchiveKeepsDataBytes 检查 readPluginArchive 只为不超过 4 MiB 的 `.txt`/`.tsv` 成员保留完整字节，其余成员仍只读文件头。
func TestPluginArchiveKeepsDataBytes(t *testing.T) {
	big := pluginNoise(maxPluginDataBytes + 1)
	members, code := readPluginArchive(pluginZip(t, pluginFile{name: "plugin.toml", data: "x"}, pluginFile{name: "words.tsv", data: strings.Repeat("w\tm\n", 1000)}, pluginFile{name: "NOTES.TXT", data: "note"}, pluginFile{name: "big.txt", data: big}, pluginFile{name: "click.wav", data: pluginWAV + strings.Repeat("\x00", 1024)}))
	if code != "" {
		t.Fatal(code)
	}
	if len(members["words.tsv"].data) != 4000 || string(members["NOTES.TXT"].data) != "note" || members["big.txt"].data != nil || members["click.wav"].data != nil || members["big.txt"].size != len(big) {
		t.Fatal(len(members["words.tsv"].data), len(members["NOTES.TXT"].data), len(members["big.txt"].data), len(members["click.wav"].data))
	}
}
