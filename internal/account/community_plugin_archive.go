package account

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
)

// Limits for community plugin packs. The pack rules mirror crates/client-core/src/plugins.rs and plugins/import.rs in the client, so a pack the server accepts installs there; the archive, decompression and quota bounds are the server's own.
const (
	maxPluginArchiveBytes   = 8 << 20
	maxPluginArchiveMembers = 64
	// The client refuses a pack folder with more regular files than this, the manifest and notices included.
	maxPluginPackFiles     = 16
	maxPluginManifestBytes = 256 << 10
	maxPluginNoticeBytes   = 64 << 10
	maxPluginFileBytes     = 16 << 20
	// The archive itself is at most 8 MiB, so this bounds how much a publish may inflate in total.
	maxPluginUnpackedBytes = 24 << 20
	// Past a small allowance, the declared uncompressed total may be at most this multiple of the archive size. Deflate tops out near 1032:1; audio and text packs stay far below 100:1, while a zip bomb does not.
	maxPluginInflateRatio  = 100
	pluginInflateAllowance = 1 << 20
	// The archive travels as standard base64 inside JSON: 8 MiB encodes to 11,184,812 bytes, and the rest covers the metadata fields.
	maxPluginPublishBytes = 11_300_000
	maxPluginsPerUser     = 20
	// Total stored archive bytes per account.
	maxPluginBytesPerUser  = 32 << 20
	pluginPublishesPerHour = 10
	// Download attempts per account per hour; each one holds up to 8 MiB of archive while it is written out.
	pluginDownloadsPerHour = 60
)

// Per-kind audio bounds, from sound_pack.rs and music_pack.rs.
const (
	maxSoundSamples     = 8
	maxSoundSampleBytes = 512 << 10
	maxSoundPackBytes   = 4 << 20
	maxSoundSemitones   = 128
	maxMusicTracks      = 8
	maxMusicTrackBytes  = 16 << 20
	maxMusicPackBytes   = 64 << 20
	maxCommandRows      = 256
	maxCommandTrigger   = 32
	maxCommandTitle     = 48
	maxCommandTemplate  = 199
)

// pluginKinds is the kind whitelist, the client's PluginKind::ALL.
var pluginKinds = []string{"sound", "music", "command_table"}

// builtinPluginIDs are the ids the client bundles for a kind; it refuses to import a pack over one of them.
var builtinPluginIDs = map[string][]string{
	"sound": {"default", "twinkle", "msime-typewriter", "msime-bubble", "msime-8bit", "msime-woodblock", "msime-pentatonic", "msime-canon", "msime-ode-to-joy"},
	"music": {"msime-music-lofi", "msime-music-ambient"},
}

// pluginPack is what a validated archive yields for storage and listing.
type pluginPack struct {
	Kind     string
	ID       string
	Name     string
	Version  string
	License  string
	Manifest []byte
}

// validPluginID is the client's safe_id: 1 to 64 lowercase ASCII letters, digits, dots, dashes and underscores, starting with a letter or digit.
func validPluginID(id string) bool {
	if id == "" || len(id) > 64 || !isASCIIAlnum(id[0]) {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func isASCIIAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// validPluginFileName is the client's valid_file_name: one plain component of at most 64 ASCII letters, digits, dots, dashes and underscores, starting with a letter or digit.
func validPluginFileName(name string) bool {
	if name == "" || len(name) > 64 || !isASCIIAlnum(name[0]) || name == "." || name == ".." {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(isASCIIAlnum(c) || c == '.' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func pluginExtension(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return ""
	}
	return strings.ToLower(name[i+1:])
}

func pluginAudio(name string) bool {
	ext := pluginExtension(name)
	return ext == "wav" || ext == "ogg"
}

func pluginNotice(name string) bool {
	ext := pluginExtension(name)
	return ext == "txt" || ext == "md"
}

// pluginNestedArchive reports a member whose name or leading bytes belong to an archive or compressed stream. Packs carry only TOML, audio and text, so any such member is refused rather than inspected.
func pluginNestedArchive(name string, head []byte) bool {
	switch pluginExtension(name) {
	case "zip", "jar", "apk", "7z", "rar", "tar", "gz", "tgz", "bz2", "xz", "zst", "lz", "lzma", "cab":
		return true
	}
	for _, magic := range []string{"PK\x03\x04", "PK\x05\x06", "PK\x07\x08", "\x1f\x8b", "BZh", "\xfd7zXZ\x00", "7z\xbc\xaf\x27\x1c", "Rar!\x1a\x07", "\x28\xb5\x2f\xfd", "MSCF"} {
		if bytes.HasPrefix(head, []byte(magic)) {
			return true
		}
	}
	return len(head) >= 262 && string(head[257:262]) == "ustar"
}

// pluginArchivePath splits a member name into components and reports whether it names a directory. ok is false for an absolute, drive-qualified, backslashed or traversing path, or one with control characters.
func pluginArchivePath(name string) (parts []string, dir bool, ok bool) {
	if name == "" || !utf8.ValidString(name) || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\:") || strings.ContainsFunc(name, unicode.IsControl) {
		return nil, false, false
	}
	dir = strings.HasSuffix(name, "/")
	parts = strings.Split(strings.TrimSuffix(name, "/"), "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, false, false
		}
	}
	return parts, dir, true
}

// pluginMember is one pack file after the archive walk: its bytes are read only as far as the checks need, the manifest in full.
type pluginMember struct {
	size int
	head []byte
	data []byte
}

// readPluginArchive walks a bounded zip archive the way the client's import does and returns the pack files by plain name. Hidden members and __MACOSX are skipped like the client skips them, but they still count toward the member cap and must have safe paths. Every pack file is inflated once, through a reader bounded by its declared size, so a forged size or a bad CRC fails here.
func readPluginArchive(archive []byte) (map[string]pluginMember, string) {
	if len(archive) < 1 || len(archive) > maxPluginArchiveBytes {
		return nil, "plugin_too_large"
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, "invalid_plugin_archive"
	}
	if len(reader.File) > maxPluginArchiveMembers {
		return nil, "plugin_too_large"
	}
	type entry struct {
		parts []string
		file  *zip.File
	}
	entries := []entry{}
	for _, f := range reader.File {
		parts, dir, ok := pluginArchivePath(f.Name)
		if !ok {
			return nil, "invalid_plugin_archive"
		}
		mode := f.Mode()
		if mode&fs.ModeSymlink != 0 || mode.Type()&^fs.ModeDir != 0 || f.Flags&0x1 != 0 {
			return nil, "invalid_plugin_archive"
		}
		if pluginNestedArchive(parts[len(parts)-1], nil) {
			return nil, "invalid_plugin_archive"
		}
		if parts[0] == "__MACOSX" || slices.ContainsFunc(parts, func(part string) bool { return strings.HasPrefix(part, ".") }) {
			continue
		}
		if dir || mode.IsDir() {
			if len(parts) > 1 {
				return nil, "invalid_plugin_archive"
			}
			continue
		}
		entries = append(entries, entry{parts, f})
	}
	// Files sit at the top level or inside one wrapper folder, decided by the first file as the client does.
	wrapper := ""
	if len(entries) > 0 && len(entries[0].parts) == 2 {
		wrapper = entries[0].parts[0]
	}
	if len(entries) > maxPluginPackFiles {
		return nil, "plugin_too_large"
	}
	members := map[string]pluginMember{}
	lowered := map[string]bool{}
	var declared uint64
	for _, e := range entries {
		var name string
		switch {
		case wrapper == "" && len(e.parts) == 1:
			name = e.parts[0]
		case wrapper != "" && len(e.parts) == 2 && e.parts[0] == wrapper:
			name = e.parts[1]
		default:
			return nil, "invalid_plugin_archive"
		}
		// Case-insensitive file systems would merge names that differ only in case.
		if !validPluginFileName(name) || lowered[strings.ToLower(name)] {
			return nil, "invalid_plugin_archive"
		}
		lowered[strings.ToLower(name)] = true
		if e.file.Method != zip.Store && e.file.Method != zip.Deflate {
			return nil, "invalid_plugin_archive"
		}
		size := e.file.UncompressedSize64
		declared += size
		if size > maxPluginFileBytes || declared > maxPluginUnpackedBytes || declared > pluginInflateAllowance+maxPluginInflateRatio*uint64(len(archive)) {
			return nil, "plugin_too_large"
		}
		members[name] = pluginMember{size: int(size)}
	}
	for _, e := range entries {
		name := e.parts[len(e.parts)-1]
		m := members[name]
		r, err := e.file.Open()
		if err != nil {
			return nil, "invalid_plugin_archive"
		}
		limited := io.LimitReader(r, int64(m.size)+1)
		head := make([]byte, 512)
		n, err := io.ReadFull(limited, head)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			r.Close()
			return nil, "invalid_plugin_archive"
		}
		m.head = head[:n]
		read := int64(n)
		if name == "plugin.toml" && m.size <= maxPluginManifestBytes {
			rest, err := io.ReadAll(limited)
			if err != nil {
				r.Close()
				return nil, "invalid_plugin_archive"
			}
			m.data = append(m.head, rest...)
			read += int64(len(rest))
		} else {
			copied, err := io.Copy(io.Discard, limited)
			if err != nil {
				r.Close()
				return nil, "invalid_plugin_archive"
			}
			read += copied
		}
		r.Close()
		if read != int64(m.size) {
			return nil, "invalid_plugin_archive"
		}
		if pluginNestedArchive(name, m.head) {
			return nil, "invalid_plugin_archive"
		}
		members[name] = m
	}
	return members, ""
}

// pluginKeys reports whether every key of table is one of allowed. It compares exactly, because go-toml matches struct fields case-insensitively and the client treats "Kind" as an unknown key.
func pluginKeys(table map[string]any, allowed ...string) bool {
	for key := range table {
		if !slices.Contains(allowed, key) {
			return false
		}
	}
	return true
}

// pluginBoundedText is the client's is_bounded_text for a required field: not blank, at most max bytes, no control characters at all.
func pluginBoundedText(s string, max int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= max && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

// pluginManifest is the typed view of plugin.toml. Pointers tell an absent key from a zero value.
type pluginManifest struct {
	SchemaVersion *int64  `toml:"schema_version"`
	Kind          *string `toml:"kind"`
	ID            *string `toml:"id"`
	Name          *string `toml:"name"`
	Version       *string `toml:"version"`
	License       *string `toml:"license"`
	Author        *string `toml:"author"`
	Description   *string `toml:"description"`
	Permissions   *[]any  `toml:"permissions"`
	Mode          *string `toml:"mode"`
	Sounds        *struct {
		Default     *string `toml:"default"`
		Space       *string `toml:"space"`
		Enter       *string `toml:"enter"`
		Backspace   *string `toml:"backspace"`
		Commit      *string `toml:"commit"`
		Achievement *string `toml:"achievement"`
	} `toml:"sounds"`
	Sequence *struct {
		Sample    *string  `toml:"sample"`
		Semitones *[]int64 `toml:"semitones"`
		Advance   *string  `toml:"advance"`
	} `toml:"sequence"`
	Music *struct {
		Tracks *[]string `toml:"tracks"`
	} `toml:"music"`
	Commands *[]struct {
		Trigger  *string `toml:"trigger"`
		Title    *string `toml:"title"`
		Template *string `toml:"template"`
	} `toml:"commands"`
}

var pluginCommonKeys = []string{"schema_version", "kind", "id", "name", "version", "license", "author", "description", "permissions"}

var pluginKindKeys = map[string][]string{
	"sound":         {"mode", "sounds", "sequence"},
	"music":         {"music"},
	"command_table": {"commands"},
}

// validPluginArchive runs every server-side check on an uploaded pack and returns what is stored, or the error code. It never decodes audio beyond its magic bytes and never executes anything.
func validPluginArchive(archive []byte) (pluginPack, string) {
	members, code := readPluginArchive(archive)
	if code != "" {
		return pluginPack{}, code
	}
	manifestFile, ok := members["plugin.toml"]
	if !ok || manifestFile.size > maxPluginManifestBytes {
		return pluginPack{}, "invalid_plugin_manifest"
	}
	raw := manifestFile.data
	if !utf8.Valid(raw) {
		return pluginPack{}, "invalid_plugin_manifest"
	}
	var table map[string]any
	if toml.Unmarshal(raw, &table) != nil {
		return pluginPack{}, "invalid_plugin_manifest"
	}
	var m pluginManifest
	decoder := toml.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m) != nil {
		return pluginPack{}, "invalid_plugin_manifest"
	}
	if m.SchemaVersion == nil || *m.SchemaVersion != 1 || m.Kind == nil || !slices.Contains(pluginKinds, *m.Kind) {
		return pluginPack{}, "invalid_plugin_manifest"
	}
	kind := *m.Kind
	if !pluginKeys(table, append(slices.Clone(pluginCommonKeys), pluginKindKeys[kind]...)...) {
		return pluginPack{}, "invalid_plugin_manifest"
	}
	if m.ID == nil || !validPluginID(*m.ID) || slices.Contains(builtinPluginIDs[kind], *m.ID) {
		return pluginPack{}, "invalid_plugin_manifest"
	}
	if m.Name == nil || !pluginBoundedText(*m.Name, 80) || m.Version == nil || !pluginBoundedText(*m.Version, 32) || m.License == nil || !pluginBoundedText(*m.License, 64) {
		return pluginPack{}, "invalid_plugin_manifest"
	}
	for _, c := range *m.License {
		if !(c < utf8.RuneSelf && isASCIIAlnum(byte(c)) || strings.ContainsRune(".-+() ", c)) {
			return pluginPack{}, "invalid_plugin_manifest"
		}
	}
	if m.Author != nil && !pluginBoundedText(*m.Author, 120) || m.Description != nil && !pluginBoundedText(*m.Description, 500) {
		return pluginPack{}, "invalid_plugin_manifest"
	}
	if m.Permissions != nil && len(*m.Permissions) > 0 {
		return pluginPack{}, "invalid_plugin_manifest"
	}
	var audio []string
	var fileLimit, totalLimit, countLimit int
	switch kind {
	case "sound":
		audio, ok = validPluginSound(table, m)
		fileLimit, totalLimit, countLimit = maxSoundSampleBytes, maxSoundPackBytes, maxSoundSamples
	case "music":
		audio, ok = validPluginMusic(table, m)
		fileLimit, totalLimit, countLimit = maxMusicTrackBytes, maxMusicPackBytes, maxMusicTracks
	case "command_table":
		ok = validPluginCommands(table, m)
	}
	if !ok {
		return pluginPack{}, "invalid_plugin_manifest"
	}
	if code := validPluginFiles(members, audio, fileLimit, totalLimit, countLimit); code != "" {
		return pluginPack{}, code
	}
	return pluginPack{Kind: kind, ID: *m.ID, Name: *m.Name, Version: *m.Version, License: *m.License, Manifest: raw}, ""
}

// validPluginFiles applies check_files: every named audio file is present, non-empty, within the kind's bounds and starts with its format's magic bytes; every other file is the manifest or a small notice.
func validPluginFiles(members map[string]pluginMember, audio []string, fileLimit, totalLimit, countLimit int) string {
	distinct := slices.Clone(audio)
	slices.Sort(distinct)
	distinct = slices.Compact(distinct)
	if len(distinct) > countLimit {
		return "invalid_plugin_manifest"
	}
	total := 0
	for _, name := range distinct {
		m, ok := members[name]
		if !ok || m.size == 0 {
			return "invalid_plugin_manifest"
		}
		if m.size > fileLimit {
			return "plugin_too_large"
		}
		total += m.size
		head := m.head
		switch pluginExtension(name) {
		case "wav":
			ok = len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WAVE"
		case "ogg":
			ok = len(head) >= 4 && string(head[:4]) == "OggS"
		default:
			ok = false
		}
		if !ok {
			return "invalid_plugin_manifest"
		}
	}
	if total > totalLimit {
		return "plugin_too_large"
	}
	for name, m := range members {
		if name == "plugin.toml" || slices.Contains(distinct, name) {
			continue
		}
		if !pluginNotice(name) {
			return "invalid_plugin_manifest"
		}
		if m.size > maxPluginNoticeBytes {
			return "plugin_too_large"
		}
	}
	return ""
}

func pluginAudioName(name *string) bool {
	return name != nil && validPluginFileName(*name) && pluginAudio(*name)
}

// validPluginSound follows sound_pack.rs: keys mode needs sounds.default and no sequence; sequence mode needs a sequence of 1 to 128 semitones in -24..24.
func validPluginSound(table map[string]any, m pluginManifest) ([]string, bool) {
	mode := "keys"
	if m.Mode != nil {
		mode = *m.Mode
	}
	if mode != "keys" && mode != "sequence" {
		return nil, false
	}
	var audio []string
	if m.Sounds != nil {
		sounds, _ := table["sounds"].(map[string]any)
		if !pluginKeys(sounds, "default", "space", "enter", "backspace", "commit", "achievement") {
			return nil, false
		}
		for _, name := range []*string{m.Sounds.Default, m.Sounds.Space, m.Sounds.Enter, m.Sounds.Backspace, m.Sounds.Commit, m.Sounds.Achievement} {
			if name == nil {
				continue
			}
			if !pluginAudioName(name) {
				return nil, false
			}
			audio = append(audio, *name)
		}
	}
	if m.Sequence != nil {
		sequence, _ := table["sequence"].(map[string]any)
		if !pluginKeys(sequence, "sample", "semitones", "advance") || !pluginAudioName(m.Sequence.Sample) || m.Sequence.Semitones == nil {
			return nil, false
		}
		semitones := *m.Sequence.Semitones
		if len(semitones) < 1 || len(semitones) > maxSoundSemitones {
			return nil, false
		}
		for _, s := range semitones {
			if s < -24 || s > 24 {
				return nil, false
			}
		}
		if m.Sequence.Advance != nil && *m.Sequence.Advance != "key" && *m.Sequence.Advance != "commit" {
			return nil, false
		}
		audio = append(audio, *m.Sequence.Sample)
	}
	switch {
	case mode == "keys" && (m.Sounds == nil || m.Sounds.Default == nil):
		return nil, false
	case mode == "keys" && m.Sequence != nil:
		return nil, false
	case mode == "sequence" && m.Sequence == nil:
		return nil, false
	}
	return audio, true
}

// validPluginMusic follows music_pack.rs: 1 to 8 distinct tracks.
func validPluginMusic(table map[string]any, m pluginManifest) ([]string, bool) {
	music, _ := table["music"].(map[string]any)
	if m.Music == nil || !pluginKeys(music, "tracks") || m.Music.Tracks == nil {
		return nil, false
	}
	tracks := *m.Music.Tracks
	if len(tracks) < 1 || len(tracks) > maxMusicTracks {
		return nil, false
	}
	for i, track := range tracks {
		if !pluginAudioName(&track) || slices.Contains(tracks[:i], track) {
			return nil, false
		}
	}
	return slices.Clone(tracks), true
}

// validPluginCommands follows command_table.rs: 1 to 256 rows with unique lowercase triggers, a short title and a template whose placeholders are {date}, {time}, {weekday}, {date:FMT} or {time:FMT}, and whose expansion stays within the client's bounds.
func validPluginCommands(table map[string]any, m pluginManifest) bool {
	rows, _ := table["commands"].([]any)
	if m.Commands == nil || len(*m.Commands) < 1 || len(*m.Commands) > maxCommandRows || len(rows) != len(*m.Commands) {
		return false
	}
	triggers := map[string]bool{}
	for i, row := range *m.Commands {
		raw, _ := rows[i].(map[string]any)
		if !pluginKeys(raw, "trigger", "title", "template") || row.Trigger == nil || row.Title == nil || row.Template == nil {
			return false
		}
		trigger := *row.Trigger
		if trigger == "" || len(trigger) > maxCommandTrigger || strings.ContainsFunc(trigger, func(c rune) bool { return c < 'a' || c > 'z' }) || triggers[trigger] {
			return false
		}
		triggers[trigger] = true
		if !pluginBoundedText(*row.Title, maxCommandTitle) {
			return false
		}
		template := *row.Template
		if strings.TrimSpace(template) == "" || len(utf16.Encode([]rune(template))) > maxCommandTemplate || strings.ContainsFunc(template, unicode.IsControl) || !validCommandTemplate(template) {
			return false
		}
	}
	return true
}

// The two instants the client expands a template at: both a Wednesday at 23:59:59 with a two-digit day, one in September for the longest English names and one in December for the two-digit month an unpadded %-m gives, so a template that fits at both fits on every day.
var commandTemplateInstants = []time.Time{time.Date(2026, time.September, 30, 23, 59, 59, 0, time.UTC), time.Date(2026, time.December, 30, 23, 59, 59, 0, time.UTC)}

// validCommandTemplate is the rest of the client's validate: the template expands at both instants, and no expansion contains a control character (%n and %t produce them) or exceeds 199 UTF-16 units.
func validCommandTemplate(template string) bool {
	for _, instant := range commandTemplateInstants {
		expanded, ok := expandCommandTemplate(template, instant)
		if !ok || strings.ContainsFunc(expanded, unicode.IsControl) || len(utf16.Encode([]rune(expanded))) > maxCommandTemplate {
			return false
		}
	}
	return true
}

// expandCommandTemplate is the client's expand_at: no stray or nested braces, only known placeholders, and every FMT one the client's strftime parser accepts and can format for a date-time without an offset.
func expandCommandTemplate(template string, t time.Time) (string, bool) {
	var out strings.Builder
	rest := template
	for {
		open := strings.IndexAny(rest, "{}")
		if open < 0 {
			out.WriteString(rest)
			return out.String(), true
		}
		if rest[open] == '}' {
			return "", false
		}
		out.WriteString(rest[:open])
		after := rest[open+1:]
		end := strings.IndexAny(after, "{}")
		if end < 0 || after[end] == '{' {
			return "", false
		}
		placeholder := after[:end]
		rest = after[end+1:]
		if placeholder == "weekday" {
			out.WriteString("星期三")
			continue
		}
		name, format, _ := strings.Cut(placeholder, ":")
		switch {
		case name == "date" && format == "":
			format = "%Y-%m-%d"
		case name == "time" && format == "":
			format = "%H:%M"
		case name != "date" && name != "time":
			return "", false
		}
		expanded, ok := pluginStrftime(format, t)
		if !ok {
			return "", false
		}
		out.WriteString(expanded)
	}
}

// pluginStrftime formats t with a strftime description the way the client does it: parse_strftime_borrowed from the time crate, then formatting a PrimitiveDateTime. ok is false for a trailing %, a specifier that parser does not know (%E, %O, %Z and the like), and %s or %z, which parse but need an offset a PrimitiveDateTime does not carry. An optional _, - or 0 after % selects space, no or zero padding for the numeric components that take it; the others ignore it.
func pluginStrftime(format string, t time.Time) (string, bool) {
	year, week := t.ISOWeek()
	hour12 := t.Hour() % 12
	if hour12 == 0 {
		hour12 = 12
	}
	period := "AM"
	if t.Hour() >= 12 {
		period = "PM"
	}
	weekday := int(t.Weekday())
	fixed := map[byte]string{
		'%': "%",
		'a': t.Weekday().String()[:3],
		'A': t.Weekday().String(),
		'b': t.Month().String()[:3],
		'h': t.Month().String()[:3],
		'B': t.Month().String(),
		'c': t.Format("Mon Jan _2 15:04:05 2006"),
		'D': t.Format("01/02/06"),
		'x': t.Format("01/02/06"),
		'F': t.Format("2006-01-02"),
		'G': fmt.Sprintf("%04d", year),
		'n': "\n",
		'p': period,
		'P': strings.ToLower(period),
		'r': fmt.Sprintf("%02d:%02d:%02d %s", hour12, t.Minute(), t.Second(), period),
		'R': t.Format("15:04"),
		't': "\t",
		'T': t.Format("15:04:05"),
		'X': t.Format("15:04:05"),
		'u': strconv.Itoa((weekday+6)%7 + 1),
		'w': strconv.Itoa(weekday + 1),
		'Y': fmt.Sprintf("%04d", t.Year()),
	}
	var out strings.Builder
	for i := 0; i < len(format); {
		if format[i] != '%' {
			next := strings.IndexByte(format[i:], '%')
			if next < 0 {
				next = len(format) - i
			}
			out.WriteString(format[i : i+next])
			i += next
			continue
		}
		if i+1 >= len(format) {
			return "", false
		}
		var pad byte
		if c := format[i+1]; c == '_' || c == '-' || c == '0' {
			pad = c
			i++
			if i+1 >= len(format) {
				return "", false
			}
		}
		component := format[i+1]
		i += 2
		number := func(value, width int, fallback byte) string {
			p := pad
			if p == 0 {
				p = fallback
			}
			digits := strconv.Itoa(value)
			switch {
			case p == '-' || len(digits) >= width:
				return digits
			case p == '_':
				return strings.Repeat(" ", width-len(digits)) + digits
			default:
				return strings.Repeat("0", width-len(digits)) + digits
			}
		}
		if text, ok := fixed[component]; ok {
			out.WriteString(text)
			continue
		}
		var text string
		switch component {
		case 'C':
			text = number(t.Year()/100, 2, '0')
		case 'd':
			text = number(t.Day(), 2, '0')
		case 'e':
			text = number(t.Day(), 2, '_')
		case 'g':
			text = number(year%100, 2, '0')
		case 'H':
			text = number(t.Hour(), 2, '0')
		case 'I':
			text = number(hour12, 2, '0')
		case 'j':
			text = number(t.YearDay(), 3, '0')
		case 'k':
			text = number(t.Hour(), 2, '_')
		case 'l':
			text = number(hour12, 2, '_')
		case 'm':
			text = number(int(t.Month()), 2, '0')
		case 'M':
			text = number(t.Minute(), 2, '0')
		case 'S':
			text = number(t.Second(), 2, '0')
		case 'U':
			text = number((t.YearDay()-weekday+6)/7, 2, '0')
		case 'V':
			text = number(week, 2, '0')
		case 'W':
			text = number((t.YearDay()-(weekday+6)%7+6)/7, 2, '0')
		case 'y':
			text = number(t.Year()%100, 2, '0')
		default:
			return "", false
		}
		out.WriteString(text)
	}
	return out.String(), true
}
