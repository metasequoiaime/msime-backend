package account

import (
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

// telemetryKinds are the accepted event kinds: download and crash from the first protocol, and the anonymous device activity kinds that feed active devices and crash-free session rates.
var telemetryKinds = map[string]bool{"download": true, "crash": true, "active": true, "session": true, "session_crash": true}

var (
	// telemetryChannel is a distribution channel key such as github or cn-mirror; the console maps known keys to labels.
	telemetryChannel = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	// telemetryInstallID is an anonymous random installation id; it must not carry user or hardware identifiers.
	telemetryInstallID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
)

// telemetryEvent is the body of POST /v1/telemetry/events. Every field added after the first protocol is optional, so older clients keep working unchanged.
type telemetryEvent struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Platform  string `json:"platform"`
	Version   string `json:"version"`
	Message   string `json:"message"`
	Stack     string `json:"stack"`
	Artifact  string `json:"artifact"`
	Channel   string `json:"channel"`
	InstallID string `json:"install_id"`
}

// valid applies the boundary rules: only crashes carry a message and stack (and must have a message), an active event must name its anonymous installation, and the optional download dimensions are short single-line values.
func (v telemetryEvent) valid() bool {
	if !resourceText(v.ID, 16, 128, false) || !telemetryKinds[v.Kind] || !resourceText(v.Platform, 1, 32, false) || !resourceText(v.Version, 1, 64, false) || !resourceText(v.Message, 0, 1000, true) || !resourceText(v.Stack, 0, 16000, true) {
		return false
	}
	if v.Kind == "crash" {
		if strings.TrimSpace(v.Message) == "" {
			return false
		}
	} else if v.Message != "" || v.Stack != "" {
		return false
	}
	if v.Artifact != "" && !resourceText(v.Artifact, 1, 64, false) {
		return false
	}
	if v.Channel != "" && !telemetryChannel.MatchString(v.Channel) {
		return false
	}
	if v.InstallID != "" && !telemetryInstallID.MatchString(v.InstallID) {
		return false
	}
	return v.Kind != "active" || v.InstallID != ""
}

// telemetryCrashTitle is the first line of a crash message, trimmed, at most 200 runes.
func telemetryCrashTitle(message string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	line = strings.TrimSpace(line)
	if utf8.RuneCountInString(line) > 200 {
		line = strings.TrimSpace(string([]rune(line)[:200]))
	}
	return line
}

// telemetryNull stores an omitted optional field as NULL, which the column checks require instead of an empty string.
func telemetryNull(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Telemetry is mounted behind the server's client/session authentication and quota. The event insert and, for a grouped crash, its crash group update share one transaction.
func (a *Service) Telemetry(w http.ResponseWriter, r *http.Request) {
	if a == nil {
		writeError(w, 503, "user_auth_disabled")
		return
	}
	var v telemetryEvent
	if !readSized(w, r, &v, 32768) {
		return
	}
	if !v.valid() {
		writeError(w, 400, "invalid_event")
		return
	}
	signature := ""
	if v.Kind == "crash" {
		signature = crashSignature(v.Message, v.Stack)
	}
	ctx := r.Context()
	tx, err := a.store.pool.Begin(ctx)
	if err != nil {
		a.error(w, err)
		return
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO admin_events(id,kind,platform,version,message,stack,artifact,channel,install_id,signature) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(id) DO NOTHING`,
		v.ID, v.Kind, v.Platform, v.Version, v.Message, v.Stack, telemetryNull(v.Artifact), telemetryNull(v.Channel), telemetryNull(v.InstallID), telemetryNull(signature))
	if err != nil {
		a.error(w, err)
		return
	}
	// A retried event is already counted in its group, so only a fresh insert touches the crash group.
	if signature != "" && tag.RowsAffected() == 1 {
		if err = a.upsertCrashGroup(ctx, tx, signature, v.Platform, v.Version, telemetryCrashTitle(v.Message)); err != nil {
			a.error(w, err)
			return
		}
	}
	if err = tx.Commit(ctx); err != nil {
		a.error(w, err)
		return
	}
	write(w, 202, map[string]bool{"accepted": true})
}
