package account

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// pluginTransferTimeout is how long a publish or a download may take end to end: either moves up to 11.3 MB of JSON, which the default 15 s route context and the server's read and write deadlines would cut off on a slow link.
const pluginTransferTimeout = 90 * time.Second

// /v1/community routes bypass the server's MaxConcurrent limit, so these slots are what bound the memory plugin transfers hold per process. A publish holds an upload slot from before its body is read until it returns, which covers the up to 11.3 MB JSON body, the decoded 8 MiB archive and its inflation; a download holds a download slot from before the archive is loaded until the response is written.
var (
	pluginUploadSlots   = make(chan struct{}, 4)
	pluginDownloadSlots = make(chan struct{}, 8)
)

// pluginPublishTurns has one entry per account with a publish waiting or running. An account's publishes take turns before they claim an upload slot, so one session cannot hold every slot with slow uploads.
var pluginPublishTurns = struct {
	sync.Mutex
	accounts map[string]*pluginPublishTurn
}{accounts: map[string]*pluginPublishTurn{}}

type pluginPublishTurn struct {
	slot    chan struct{}
	waiting int
}

// acquirePluginSlot waits for a free slot in slots until ctx ends and returns its release, or false when ctx ended first.
func acquirePluginSlot(ctx context.Context, slots chan struct{}) (func(), bool) {
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, true
	case <-ctx.Done():
		return nil, false
	}
}

// acquirePluginPublishTurn waits until no other publish of userID is running and returns the release, or false when ctx ended first.
func acquirePluginPublishTurn(ctx context.Context, userID string) (func(), bool) {
	pluginPublishTurns.Lock()
	turn := pluginPublishTurns.accounts[userID]
	if turn == nil {
		turn = &pluginPublishTurn{slot: make(chan struct{}, 1)}
		pluginPublishTurns.accounts[userID] = turn
	}
	turn.waiting++
	pluginPublishTurns.Unlock()
	leave := func() {
		pluginPublishTurns.Lock()
		if turn.waiting--; turn.waiting == 0 {
			delete(pluginPublishTurns.accounts, userID)
		}
		pluginPublishTurns.Unlock()
	}
	release, ok := acquirePluginSlot(ctx, turn.slot)
	if !ok {
		leave()
		return nil, false
	}
	return func() {
		release()
		leave()
	}, true
}

// CommunityPlugin is the list and detail item; it never carries the archive or manifest bytes. Owned and MyRating are false and 0 for an anonymous viewer.
type CommunityPlugin struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	PluginID      string    `json:"plugin_id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	Author        string    `json:"author"`
	Version       string    `json:"version"`
	License       string    `json:"license"`
	Size          int64     `json:"size"`
	SHA256        string    `json:"sha256"`
	Downloads     int       `json:"downloads"`
	RatingCount   int       `json:"rating_count"`
	RatingAverage float64   `json:"rating_average"`
	Owned         bool      `json:"owned"`
	MyRating      int       `json:"my_rating"`
	CreatedAt     time.Time `json:"created_at"`
}

const pluginSelect = `SELECT p.id,p.kind,p.plugin_id,p.name,p.description,
 COALESCE(NULLIF(btrim(u.display_name),''),'水杉小鹿·'||upper(left(u.id,6))),p.version,p.license,p.size,p.sha256,
 (SELECT count(*) FROM community_plugin_downloads WHERE pack_id=p.id),
 (SELECT count(*) FROM community_plugin_ratings WHERE pack_id=p.id),
 COALESCE((SELECT avg(stars) FROM community_plugin_ratings WHERE pack_id=p.id),0),
 p.owner_id=$1,COALESCE((SELECT stars FROM community_plugin_ratings WHERE pack_id=p.id AND user_id=$1),0),p.created_at
 FROM community_plugins p JOIN auth_users u ON u.id=p.owner_id `

func scanCommunityPlugin(row interface{ Scan(...any) error }) (CommunityPlugin, error) {
	var p CommunityPlugin
	err := row.Scan(&p.ID, &p.Kind, &p.PluginID, &p.Name, &p.Description, &p.Author, &p.Version, &p.License, &p.Size, &p.SHA256, &p.Downloads, &p.RatingCount, &p.RatingAverage, &p.Owned, &p.MyRating, &p.CreatedAt)
	return p, err
}

// pluginRequestDigest identifies one publish request, so an identical retry is answered with the stored row and anything else under the same id is a conflict.
func pluginRequestDigest(name, description, kind, pluginID, version string, archive []byte) string {
	sum := sha256.Sum256(archive)
	h := sha256.New()
	for _, part := range []string{name, description, kind, pluginID, version, hex.EncodeToString(sum[:])} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func extendPluginTransfer(w http.ResponseWriter) {
	// Extend only this request's socket deadlines, as the candidate-skin publish does; Mount gives the route the matching context.
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(pluginTransferTimeout))
	_ = controller.SetWriteDeadline(time.Now().Add(pluginTransferTimeout + 5*time.Second))
}

func (a *Service) communityPluginList(w http.ResponseWriter, r *http.Request) {
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 0 || n > 100000 {
			writeError(w, 400, "invalid_offset")
			return
		}
		offset = n
	}
	search := r.URL.Query().Get("q")
	if !utf8.ValidString(search) || len(search) > 128 {
		writeError(w, 400, "invalid_search")
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != "" && !slices.Contains(pluginKinds, kind) {
		writeError(w, 400, "invalid_kind")
		return
	}
	rows, e := a.store.pool.Query(r.Context(), pluginSelect+`WHERE strpos(lower(p.name),lower($2))>0 AND ($3='' OR p.kind=$3) AND (p.moderation<>'removed' OR p.owner_id=$1) ORDER BY p.created_at DESC,p.id LIMIT 21 OFFSET $4`, a.communityViewer(r), search, kind, offset)
	if e != nil {
		a.error(w, e)
		return
	}
	defer rows.Close()
	items := []CommunityPlugin{}
	for rows.Next() {
		v, e := scanCommunityPlugin(rows)
		if e != nil {
			a.error(w, e)
			return
		}
		items = append(items, v)
	}
	if e = rows.Err(); e != nil {
		a.error(w, e)
		return
	}
	more := len(items) > 20
	if more {
		items = items[:20]
	}
	write(w, 200, map[string]any{"plugins": items, "has_more": more})
}

func (a *Service) communityPluginDetail(w http.ResponseWriter, r *http.Request) {
	v, e := scanCommunityPlugin(a.store.pool.QueryRow(r.Context(), pluginSelect+`WHERE p.id=$2 AND (p.moderation<>'removed' OR p.owner_id=$1)`, a.communityViewer(r), r.PathValue("id")))
	if errors.Is(e, pgx.ErrNoRows) {
		writeError(w, 404, "plugin_not_found")
		return
	}
	if e != nil {
		a.error(w, e)
		return
	}
	write(w, 200, v)
}

func (a *Service) communityPluginPublish(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r, false)
	if !ok {
		return
	}
	extendPluginTransfer(w)
	releaseTurn, ok := acquirePluginPublishTurn(r.Context(), p.UserID)
	if !ok {
		writeError(w, 503, "plugin_busy")
		return
	}
	defer releaseTurn()
	releaseSlot, ok := acquirePluginSlot(r.Context(), pluginUploadSlots)
	if !ok {
		writeError(w, 503, "plugin_busy")
		return
	}
	defer releaseSlot()
	var input struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Kind        string `json:"kind"`
		PluginID    string `json:"plugin_id"`
		Version     string `json:"version"`
		// Standard base64 of the zip archive.
		Archive []byte `json:"archive"`
	}
	if !readSized(w, r, &input, maxPluginPublishBytes) {
		return
	}
	input.ID = strings.ToLower(input.ID)
	if len(input.ID) != 36 || !validCommunityID(input.ID) {
		writeError(w, 400, "invalid_community_id")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	// Clients reject a listed name with any control character and a description with one other than newline or tab.
	if !resourceText(input.Name, 1, 32, false) || !resourceText(input.Description, 0, 280, true) {
		writeError(w, 400, "invalid_plugin_metadata")
		return
	}
	if !slices.Contains(pluginKinds, input.Kind) {
		writeError(w, 400, "invalid_kind")
		return
	}
	if !validPluginID(input.PluginID) || !pluginBoundedText(input.Version, 32) {
		writeError(w, 400, "invalid_plugin_metadata")
		return
	}
	if len(input.Archive) < 1 || len(input.Archive) > maxPluginArchiveBytes {
		writeError(w, 400, "plugin_too_large")
		return
	}
	digest := pluginRequestDigest(input.Name, input.Description, input.Kind, input.PluginID, input.Version, input.Archive)
	// A retry of a publication that already committed is answered before the archive is inflated or the hourly rate is charged, so a lost response can never turn into 429. The locked probe in the transaction stays authoritative.
	var existingOwner, existingDigest string
	e := a.store.pool.QueryRow(r.Context(), `SELECT owner_id,request_sha256 FROM community_plugins WHERE id=$1`, input.ID).Scan(&existingOwner, &existingDigest)
	if e == nil {
		if existingOwner != p.UserID || existingDigest != digest {
			writeError(w, 409, "plugin_id_conflict")
			return
		}
		v, e := scanCommunityPlugin(a.store.pool.QueryRow(r.Context(), pluginSelect+`WHERE p.id=$2`, p.UserID, input.ID))
		if e != nil {
			a.error(w, e)
			return
		}
		write(w, 200, v)
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		a.error(w, e)
		return
	}
	flag, ok := a.screenUpload(w, r, input.Name, input.Description)
	if !ok {
		return
	}
	// Charged before the archive is inflated, as the candidate-skin publish charges before decoding, so rejected uploads cannot loop on server CPU.
	if e := a.RateLimit(r.Context(), "plugin-publish", p.UserID, pluginPublishesPerHour, time.Hour); e != nil {
		a.error(w, e)
		return
	}
	pack, code := validPluginArchive(input.Archive)
	if code != "" {
		writeError(w, 400, code)
		return
	}
	if pack.Kind != input.Kind {
		writeError(w, 400, "plugin_kind_mismatch")
		return
	}
	if pack.ID != input.PluginID || pack.Version != input.Version {
		writeError(w, 400, "plugin_manifest_mismatch")
		return
	}
	// The account row lock serialises this account's publishes, so the count and byte quotas cannot both pass for two concurrent uploads.
	tx, e := a.store.userDataTransaction(r.Context(), p.UserID)
	if e != nil {
		a.error(w, e)
		return
	}
	defer tx.Rollback(r.Context())
	e = tx.QueryRow(r.Context(), `SELECT owner_id,request_sha256 FROM community_plugins WHERE id=$1`, input.ID).Scan(&existingOwner, &existingDigest)
	if e == nil {
		if existingOwner != p.UserID || existingDigest != digest {
			writeError(w, 409, "plugin_id_conflict")
			return
		}
		v, e := scanCommunityPlugin(tx.QueryRow(r.Context(), pluginSelect+`WHERE p.id=$2`, p.UserID, input.ID))
		if e != nil {
			a.error(w, e)
			return
		}
		write(w, 200, v)
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		a.error(w, e)
		return
	}
	var count, stored int64
	if e = tx.QueryRow(r.Context(), `SELECT count(*),COALESCE(sum(size),0) FROM community_plugins WHERE owner_id=$1`, p.UserID).Scan(&count, &stored); e != nil {
		a.error(w, e)
		return
	}
	if count >= maxPluginsPerUser {
		writeError(w, 409, "plugin_publish_limit")
		return
	}
	if stored+int64(len(input.Archive)) > maxPluginBytesPerUser {
		writeError(w, 409, "plugin_storage_limit")
		return
	}
	// The row lock covers only this account, so another account can commit the same id between the probe and this insert; ON CONFLICT waits for that commit and then reports it as a conflict instead of a unique violation.
	inserted, e := tx.Exec(r.Context(), `INSERT INTO community_plugins(id,owner_id,kind,plugin_id,name,description,version,license,manifest,archive,request_sha256,moderation,moderation_reason) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'pending',$12) ON CONFLICT(id) DO NOTHING`,
		input.ID, p.UserID, pack.Kind, pack.ID, input.Name, input.Description, pack.Version, pack.License, pack.Manifest, input.Archive, digest, flag)
	if e != nil {
		a.error(w, e)
		return
	}
	if inserted.RowsAffected() == 0 {
		writeError(w, 409, "plugin_id_conflict")
		return
	}
	v, e := scanCommunityPlugin(tx.QueryRow(r.Context(), pluginSelect+`WHERE p.id=$2`, p.UserID, input.ID))
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		a.error(w, e)
		return
	}
	write(w, 201, v)
}

func (a *Service) communityPluginDownload(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r, false)
	if !ok {
		return
	}
	// Charged before the slot is claimed, so repeated downloads from one account cannot keep the slots busy; the count of downloaders stays deduplicated separately.
	if e := a.RateLimit(r.Context(), "plugin-download", p.UserID, pluginDownloadsPerHour, time.Hour); e != nil {
		a.error(w, e)
		return
	}
	extendPluginTransfer(w)
	releaseSlot, ok := acquirePluginSlot(r.Context(), pluginDownloadSlots)
	if !ok {
		writeError(w, 503, "plugin_busy")
		return
	}
	defer releaseSlot()
	tx, e := a.store.pool.Begin(r.Context())
	if e != nil {
		a.error(w, e)
		return
	}
	defer tx.Rollback(r.Context())
	var kind, pluginID, version, sum string
	var archive []byte
	e = tx.QueryRow(r.Context(), `SELECT kind,plugin_id,version,sha256,archive FROM community_plugins WHERE id=$1 AND (moderation<>'removed' OR owner_id=$2) FOR SHARE`, r.PathValue("id"), p.UserID).Scan(&kind, &pluginID, &version, &sum, &archive)
	if errors.Is(e, pgx.ErrNoRows) {
		writeError(w, 404, "plugin_not_found")
		return
	}
	if e != nil {
		a.error(w, e)
		return
	}
	_, e = tx.Exec(r.Context(), `INSERT INTO community_plugin_downloads(pack_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, r.PathValue("id"), p.UserID)
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		a.error(w, e)
		return
	}
	writePluginDownload(w, pluginDownload{ID: r.PathValue("id"), Kind: kind, PluginID: pluginID, Version: version, Size: len(archive), SHA256: sum}, archive)
}

// pluginDownload is the download response without its archive field, which writePluginDownload appends.
type pluginDownload struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	PluginID string `json:"plugin_id"`
	Version  string `json:"version"`
	Size     int    `json:"size"`
	SHA256   string `json:"sha256"`
}

// writePluginDownload writes the same JSON object write would, but streams the archive through a base64 encoder instead of letting json.Encoder build a second, 4/3-sized copy of it in memory before the first byte goes out.
func writePluginDownload(w http.ResponseWriter, head pluginDownload, archive []byte) {
	fields, _ := json.Marshal(head)
	prefix := append(fields[:len(fields)-1], `,"archive":"`...)
	suffix := "\"}\n"
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Length", strconv.Itoa(len(prefix)+base64.StdEncoding.EncodedLen(len(archive))+len(suffix)))
	w.WriteHeader(200)
	// A failed write means the client went away; there is nothing left to report to it.
	if _, e := w.Write(prefix); e != nil {
		return
	}
	encoder := base64.NewEncoder(base64.StdEncoding, w)
	if _, e := encoder.Write(archive); e != nil {
		return
	}
	if e := encoder.Close(); e != nil {
		return
	}
	_, _ = io.WriteString(w, suffix)
}

func (a *Service) communityPluginRate(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r, false)
	if !ok {
		return
	}
	var input struct {
		Stars int `json:"stars"`
	}
	if !read(w, r, &input) {
		return
	}
	if input.Stars < 1 || input.Stars > 5 {
		writeError(w, 400, "invalid_rating")
		return
	}
	result, e := a.store.pool.Exec(r.Context(), `INSERT INTO community_plugin_ratings(pack_id,user_id,stars)
 SELECT p.id,$2,$3 FROM community_plugins p WHERE p.id=$1 AND p.owner_id<>$2 AND p.moderation<>'removed' AND EXISTS(SELECT 1 FROM community_plugin_downloads WHERE pack_id=p.id AND user_id=$2)
 ON CONFLICT(pack_id,user_id) DO UPDATE SET stars=excluded.stars`, r.PathValue("id"), p.UserID, input.Stars)
	if e != nil {
		a.error(w, e)
		return
	}
	if result.RowsAffected() == 0 {
		// A missing pack is 404; an existing one the caller owns or has not downloaded is 403.
		var exists bool
		if e = a.store.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM community_plugins WHERE id=$1 AND moderation<>'removed')`, r.PathValue("id")).Scan(&exists); e != nil {
			a.error(w, e)
			return
		}
		if !exists {
			writeError(w, 404, "plugin_not_found")
			return
		}
		writeError(w, 403, "download_before_rating_or_own_plugin")
		return
	}
	write(w, 200, map[string]int{"stars": input.Stars})
}

func (a *Service) communityPluginDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r, false)
	if !ok {
		return
	}
	result, e := a.store.pool.Exec(r.Context(), `DELETE FROM community_plugins WHERE id=$1 AND owner_id=$2`, r.PathValue("id"), p.UserID)
	if e != nil {
		a.error(w, e)
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, 404, "plugin_not_found")
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
