package account

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/metasequoiaime/MSIME-Backend/internal/skins"
	"github.com/pelletier/go-toml/v2"
)

// Limits for user-published candidate-window skin packages. crates/client-core/src/skin/candidate_community.rs mirrors them so the client can refuse a package before uploading it.
const (
	// The manifest can reference only the preview, the decoration image and the background image, and every uploaded file must be referenced, so three files is the real bound.
	maxCandidateFiles         = 3
	maxCandidateFileBytes     = 1 << 20
	maxCandidatePackageBytes  = 2 << 20
	maxCandidatePreviewBytes  = 256 << 10
	maxCandidateManifestBytes = 65536
	maxCandidateSide          = 2048
	maxCandidatePixels        = 8_000_000
	// Go's JPEG decoder walks every block of the image once per scan and sets no limit on the scan count, so decode cost is scans times pixels and a small file with thousands of scans pins a core. Standard progressive scripts use about ten scans and optimising encoders stay near twenty.
	maxCandidateJPEGScans = 32
	// Covers 2 MiB of images in base64 (2,796,204 bytes) plus a JSON-escaped 64 KiB manifest, keys and metadata.
	maxCandidatePublishBytes  = 3_200_000
	maxCandidateSkinsPerUser  = 20
	candidatePublishesPerHour = 10
)

// candidateImageSlots bounds concurrent image decoding per process, because /v1/community routes bypass the server's MaxConcurrent limit and a package can decode up to 8 MP.
var candidateImageSlots = make(chan struct{}, 2)

// candidatePublishTimeout is how long one publish may take end to end: up to 3.2 MB of upload, then decoding and re-encoding up to 8 MP. The default 15 s route context and the server's read deadline would cut a slow uplink off mid-upload; the client waits 90 s.
const candidatePublishTimeout = 90 * time.Second

func acquireCandidateImageSlot(ctx context.Context) bool {
	select {
	case candidateImageSlots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// withCandidateImageSlot runs work while holding an image slot and gives the slot back however work ends, a panic included, so a failed decode can never shrink the pool. It reports false, without running work, when ctx ends before a slot frees.
func withCandidateImageSlot(ctx context.Context, work func()) bool {
	if !acquireCandidateImageSlot(ctx) {
		return false
	}
	defer func() { <-candidateImageSlots }()
	work()
	return true
}

type CandidateSkinLicense struct {
	Code   string `json:"code"`
	Assets string `json:"assets"`
	Source string `json:"source"`
}

// CommunityCandidateSkin is the list and detail item; it never carries the manifest or image bytes.
type CommunityCandidateSkin struct {
	ID            string               `json:"id"`
	PackageID     string               `json:"package_id"`
	Name          string               `json:"name"`
	Description   string               `json:"description"`
	Author        string               `json:"author"`
	Version       string               `json:"version"`
	License       CandidateSkinLicense `json:"license"`
	Size          int64                `json:"size"`
	FileCount     int                  `json:"file_count"`
	Downloads     int                  `json:"downloads"`
	RatingCount   int                  `json:"rating_count"`
	RatingAverage float64              `json:"rating_average"`
	Owned         bool                 `json:"owned"`
	MyRating      int                  `json:"my_rating"`
	CreatedAt     time.Time            `json:"created_at"`
}

const candidateSelect = `SELECT s.id,s.package_id,s.name,s.description,
 COALESCE(NULLIF(btrim(u.display_name),''),'水杉小鹿·'||upper(left(u.id,6))),s.version,s.license_code,s.license_assets,s.license_source,
 f.size,f.files,
 (SELECT count(*) FROM community_candidate_skin_downloads WHERE skin_id=s.id),
 (SELECT count(*) FROM community_candidate_skin_ratings WHERE skin_id=s.id),
 COALESCE((SELECT avg(stars) FROM community_candidate_skin_ratings WHERE skin_id=s.id),0),
 s.owner_id=$1,COALESCE((SELECT stars FROM community_candidate_skin_ratings WHERE skin_id=s.id AND user_id=$1),0),s.created_at
 FROM community_candidate_skins s JOIN auth_users u ON u.id=s.owner_id
 CROSS JOIN LATERAL (SELECT COALESCE(sum(size),0) AS size,count(*) AS files FROM community_candidate_skin_files WHERE skin_id=s.id) f `

func scanCandidateSkin(row interface{ Scan(...any) error }) (CommunityCandidateSkin, error) {
	var s CommunityCandidateSkin
	err := row.Scan(&s.ID, &s.PackageID, &s.Name, &s.Description, &s.Author, &s.Version, &s.License.Code, &s.License.Assets, &s.License.Source, &s.Size, &s.FileCount, &s.Downloads, &s.RatingCount, &s.RatingAverage, &s.Owned, &s.MyRating, &s.CreatedAt)
	return s, err
}

// candidateExtension is the lowercased extension of an accepted image path, or "" when the path is not png, jpg or jpeg.
func candidateExtension(path string) string {
	i := strings.LastIndexByte(path, '.')
	if i < 0 {
		return ""
	}
	switch ext := strings.ToLower(path[i+1:]); ext {
	case "png":
		return "png"
	case "jpg", "jpeg":
		return "jpeg"
	}
	return ""
}

// validCandidateFiles runs the structural checks that need no image decode and returns the error code, or "" when the upload may proceed.
func validCandidateFiles(manifest string, files map[string][]byte) string {
	if len(files) < 1 || len(files) > maxCandidateFiles {
		return "candidate_skin_file_path"
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	lowered := map[string]bool{}
	total := 0
	for _, path := range paths {
		if !skins.SafeResource(path, 256) || path == "skin.toml" {
			return "candidate_skin_file_path"
		}
		if candidateExtension(path) == "" {
			return "candidate_skin_file_type"
		}
		if lowered[strings.ToLower(path)] {
			return "candidate_skin_file_path"
		}
		lowered[strings.ToLower(path)] = true
		size := len(files[path])
		total += size
		if size < 1 || size > maxCandidateFileBytes || total > maxCandidatePackageBytes {
			return "candidate_skin_too_large"
		}
	}
	if len(manifest) > maxCandidateManifestBytes {
		return "candidate_skin_too_large"
	}
	return ""
}

// candidateRequestDigest identifies one publish request by its original bytes, so an identical retry is recognised even though the stored images are re-encoded.
func candidateRequestDigest(name, description, manifest string, files map[string][]byte) string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	h := sha256.New()
	for _, part := range []string{name, description, manifest} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	for _, path := range paths {
		sum := sha256.Sum256(files[path])
		h.Write([]byte(path + "\x00" + hex.EncodeToString(sum[:]) + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// validCandidatePackage validates the manifest with the shared client-dialect port, then applies the v1 sharing rules around it: images only, a required image preview, a declared asset license, and no unreferenced files.
func validCandidatePackage(manifest string, files map[string][]byte) (skins.Package, string) {
	var head struct {
		ID string `toml:"id"`
	}
	if toml.Unmarshal([]byte(manifest), &head) != nil || head.ID == "" {
		return skins.Package{}, "invalid_candidate_skin_package"
	}
	resources := make([]skins.StoredResource, 0, len(files))
	for path, data := range files {
		sum := sha256.Sum256(data)
		resources = append(resources, skins.StoredResource{Path: path, Size: len(data), SHA256: hex.EncodeToString(sum[:])})
	}
	pkg, err := skins.ParseStored(skins.Stored{ID: head.ID, Manifest: []byte(manifest), Resources: resources})
	if err != nil {
		return pkg, "invalid_candidate_skin_package"
	}
	if pkg.ToolbarStylesheet != "" {
		return pkg, "candidate_skin_file_type"
	}
	if pkg.Preview == "" {
		return pkg, "candidate_skin_preview_required"
	}
	if _, uploaded := files[pkg.Preview]; !uploaded || candidateExtension(pkg.Preview) == "" {
		return pkg, "candidate_skin_file_type"
	}
	if pkg.License == nil || strings.TrimSpace(pkg.License.Assets) == "" {
		return pkg, "candidate_skin_license_required"
	}
	// ParseStored bounds these strings by length only, while clients reject a listed item whose version or license carries a control character, so one such row would fail every gallery page that contains it.
	for _, text := range []string{pkg.Version, pkg.License.Code, pkg.License.Assets, pkg.License.Source} {
		if strings.ContainsFunc(text, unicode.IsControl) {
			return pkg, "invalid_candidate_skin_package"
		}
	}
	referenced := map[string]bool{pkg.Preview: true}
	if decoration := pkg.CandidateWindow.Decoration.Image; decoration != "" {
		referenced[decoration] = true
	}
	if background := pkg.CandidateWindow.Background; background != nil {
		referenced[background.Image] = true
	}
	if len(referenced) != len(files) {
		return pkg, "invalid_candidate_skin_package"
	}
	for path := range files {
		if !referenced[path] {
			return pkg, "invalid_candidate_skin_package"
		}
	}
	return pkg, ""
}

// candidateJPEGScans counts the SOS markers the Go decoder would process, following its marker loop: bytes outside a marker are skipped, fill bytes and restart markers carry no length, and the entropy-coded data after an SOS header ends at the first 0xFF that is not followed by 0x00 or a restart marker. It stops at EOI or at the end of the data and leaves structural errors to the decoder.
func candidateJPEGScans(data []byte) int {
	scans := 0
	for i := 2; i+1 < len(data); {
		if data[i] != 0xFF {
			i++
			continue
		}
		marker := data[i+1]
		switch {
		case marker == 0xFF:
			i++
			continue
		case marker == 0xD9:
			return scans
		case marker == 0x00 || marker >= 0xD0 && marker <= 0xD7:
			i += 2
			continue
		}
		if i+3 >= len(data) {
			return scans
		}
		i += 2 + (int(data[i+2])<<8 | int(data[i+3]))
		if marker != 0xDA {
			continue
		}
		scans++
		for i+1 < len(data) && (data[i] != 0xFF || data[i+1] == 0x00 || data[i+1] >= 0xD0 && data[i+1] <= 0xD7) {
			i++
		}
	}
	return scans
}

// sanitizeCandidateImages decodes every image with the codec its extension names and re-encodes it, which drops EXIF, XMP, ICC and text chunks and flattens APNG to its first frame. Dimensions and JPEG scan counts are checked from the headers before any full decode.
func sanitizeCandidateImages(files map[string][]byte, preview string) (map[string][]byte, string) {
	pixels := 0
	for path, data := range files {
		var config image.Config
		var err error
		if candidateExtension(path) == "png" {
			config, err = png.DecodeConfig(bytes.NewReader(data))
		} else if candidateJPEGScans(data) > maxCandidateJPEGScans {
			return nil, "candidate_skin_image_invalid"
		} else {
			config, err = jpeg.DecodeConfig(bytes.NewReader(data))
		}
		if err != nil {
			return nil, "candidate_skin_image_invalid"
		}
		if config.Width < 1 || config.Height < 1 || config.Width > maxCandidateSide || config.Height > maxCandidateSide {
			return nil, "candidate_skin_too_large"
		}
		pixels += config.Width * config.Height
		if pixels > maxCandidatePixels {
			return nil, "candidate_skin_too_large"
		}
	}
	clean := make(map[string][]byte, len(files))
	total := 0
	for path, data := range files {
		var decoded image.Image
		var err error
		var out bytes.Buffer
		if candidateExtension(path) == "png" {
			if decoded, err = png.Decode(bytes.NewReader(data)); err == nil {
				err = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, decoded)
			}
		} else {
			if decoded, err = jpeg.Decode(bytes.NewReader(data)); err == nil {
				err = jpeg.Encode(&out, decoded, &jpeg.Options{Quality: 90})
			}
		}
		if err != nil {
			return nil, "candidate_skin_image_invalid"
		}
		total += out.Len()
		if out.Len() > maxCandidateFileBytes || total > maxCandidatePackageBytes || (path == preview && out.Len() > maxCandidatePreviewBytes) {
			return nil, "candidate_skin_too_large"
		}
		clean[path] = out.Bytes()
	}
	return clean, ""
}

func (a *Service) communityCandidateList(w http.ResponseWriter, r *http.Request) {
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
	scope := r.URL.Query().Get("scope")
	if scope != "" && scope != "mine" {
		writeError(w, 400, "invalid_scope")
		return
	}
	viewer := a.communityViewer(r)
	if scope == "mine" && viewer == "" {
		writeError(w, 401, "user_session_required")
		return
	}
	rows, e := a.store.pool.Query(r.Context(), candidateSelect+`WHERE strpos(lower(s.name),lower($2))>0 AND ($3='' OR s.owner_id=$1) ORDER BY s.created_at DESC,s.id LIMIT 21 OFFSET $4`, viewer, search, scope, offset)
	if e != nil {
		a.error(w, e)
		return
	}
	defer rows.Close()
	items := []CommunityCandidateSkin{}
	for rows.Next() {
		v, e := scanCandidateSkin(rows)
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
	write(w, 200, map[string]any{"skins": items, "has_more": more})
}
func (a *Service) communityCandidateDetail(w http.ResponseWriter, r *http.Request) {
	v, e := scanCandidateSkin(a.store.pool.QueryRow(r.Context(), candidateSelect+`WHERE s.id=$2`, a.communityViewer(r), r.PathValue("id")))
	if errors.Is(e, pgx.ErrNoRows) {
		writeError(w, 404, "skin_not_found")
		return
	}
	if e != nil {
		a.error(w, e)
		return
	}
	write(w, 200, v)
}
func (a *Service) communityCandidatePreview(w http.ResponseWriter, r *http.Request) {
	var path string
	var data []byte
	e := a.store.pool.QueryRow(r.Context(), `SELECT f.path,f.bytes FROM community_candidate_skins s JOIN community_candidate_skin_files f ON f.skin_id=s.id AND f.path=s.preview_path WHERE s.id=$1`, r.PathValue("id")).Scan(&path, &data)
	if errors.Is(e, pgx.ErrNoRows) {
		writeError(w, 404, "skin_not_found")
		return
	}
	if e != nil {
		a.error(w, e)
		return
	}
	write(w, 200, map[string]any{"path": path, "content_type": "image/" + candidateExtension(path), "data": data})
}
func (a *Service) communityCandidatePublish(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r, false)
	if !ok {
		return
	}
	// Extend only this upload's socket deadlines, as the dictionary snapshot restore does; Mount gives the route the matching context.
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(candidatePublishTimeout))
	_ = controller.SetWriteDeadline(time.Now().Add(candidatePublishTimeout + 5*time.Second))
	var input struct {
		ID          string            `json:"id"`
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Manifest    string            `json:"manifest"`
		Files       map[string][]byte `json:"files"`
	}
	if !readSized(w, r, &input, maxCandidatePublishBytes) {
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
		writeError(w, 400, "invalid_skin_metadata")
		return
	}
	if code := validCandidateFiles(input.Manifest, input.Files); code != "" {
		writeError(w, 400, code)
		return
	}
	digest := candidateRequestDigest(input.Name, input.Description, input.Manifest, input.Files)
	pkg, code := validCandidatePackage(input.Manifest, input.Files)
	if code != "" {
		writeError(w, 400, code)
		return
	}
	// A retry of a publication that already committed is answered before the hourly rate is charged, so a lost response can never turn into 429. The locked probe in the transaction stays authoritative.
	var existingOwner, existingDigest string
	e := a.store.pool.QueryRow(r.Context(), `SELECT owner_id,request_sha256 FROM community_candidate_skins WHERE id=$1`, input.ID).Scan(&existingOwner, &existingDigest)
	if e == nil {
		if existingOwner != p.UserID || existingDigest != digest {
			writeError(w, 409, "candidate_skin_id_conflict")
			return
		}
		v, e := scanCandidateSkin(a.store.pool.QueryRow(r.Context(), candidateSelect+`WHERE s.id=$2`, p.UserID, input.ID))
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
	if e := a.RateLimit(r.Context(), "candidate-publish", p.UserID, candidatePublishesPerHour, time.Hour); e != nil {
		a.error(w, e)
		return
	}
	var clean map[string][]byte
	if !withCandidateImageSlot(r.Context(), func() { clean, code = sanitizeCandidateImages(input.Files, pkg.Preview) }) {
		writeError(w, 503, "candidate_skin_busy")
		return
	}
	if code != "" {
		writeError(w, 400, code)
		return
	}
	tx, e := a.store.userDataTransaction(r.Context(), p.UserID)
	if e != nil {
		a.error(w, e)
		return
	}
	defer tx.Rollback(r.Context())
	// The client UUID makes a publication retry safe and never overwrites someone else's work.
	e = tx.QueryRow(r.Context(), `SELECT owner_id,request_sha256 FROM community_candidate_skins WHERE id=$1`, input.ID).Scan(&existingOwner, &existingDigest)
	if e == nil {
		if existingOwner != p.UserID || existingDigest != digest {
			writeError(w, 409, "candidate_skin_id_conflict")
			return
		}
		v, e := scanCandidateSkin(tx.QueryRow(r.Context(), candidateSelect+`WHERE s.id=$2`, p.UserID, input.ID))
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
	var count int
	if e = tx.QueryRow(r.Context(), `SELECT count(*) FROM community_candidate_skins WHERE owner_id=$1`, p.UserID).Scan(&count); e != nil {
		a.error(w, e)
		return
	}
	if count >= maxCandidateSkinsPerUser {
		writeError(w, 409, "candidate_skin_publish_limit")
		return
	}
	license := CandidateSkinLicense{Code: pkg.License.Code, Assets: pkg.License.Assets, Source: pkg.License.Source}
	// The row lock covers only this account, so another account can commit the same id between the probe and this insert; ON CONFLICT waits for that commit and then reports it as a conflict instead of a unique violation.
	inserted, e := tx.Exec(r.Context(), `INSERT INTO community_candidate_skins(id,owner_id,package_id,name,description,version,license_code,license_assets,license_source,manifest,preview_path,request_sha256) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(id) DO NOTHING`,
		input.ID, p.UserID, pkg.ID, input.Name, input.Description, pkg.Version, license.Code, license.Assets, license.Source, []byte(input.Manifest), pkg.Preview, digest)
	if e != nil {
		a.error(w, e)
		return
	}
	if inserted.RowsAffected() == 0 {
		writeError(w, 409, "candidate_skin_id_conflict")
		return
	}
	paths := make([]string, 0, len(clean))
	for path := range clean {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		if _, e = tx.Exec(r.Context(), `INSERT INTO community_candidate_skin_files(skin_id,path,bytes) VALUES($1,$2,$3)`, input.ID, path, clean[path]); e != nil {
			a.error(w, e)
			return
		}
	}
	v, e := scanCandidateSkin(tx.QueryRow(r.Context(), candidateSelect+`WHERE s.id=$2`, p.UserID, input.ID))
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		a.error(w, e)
		return
	}
	write(w, 201, v)
}
func (a *Service) communityCandidateDownload(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r, false)
	if !ok {
		return
	}
	tx, e := a.store.pool.Begin(r.Context())
	if e != nil {
		a.error(w, e)
		return
	}
	defer tx.Rollback(r.Context())
	var packageID string
	var manifest []byte
	e = tx.QueryRow(r.Context(), `SELECT package_id,manifest FROM community_candidate_skins WHERE id=$1 FOR SHARE`, r.PathValue("id")).Scan(&packageID, &manifest)
	if errors.Is(e, pgx.ErrNoRows) {
		writeError(w, 404, "skin_not_found")
		return
	}
	if e != nil {
		a.error(w, e)
		return
	}
	rows, e := tx.Query(r.Context(), `SELECT path,bytes FROM community_candidate_skin_files WHERE skin_id=$1 ORDER BY path`, r.PathValue("id"))
	if e != nil {
		a.error(w, e)
		return
	}
	files := map[string][]byte{}
	for rows.Next() {
		var path string
		var data []byte
		if e = rows.Scan(&path, &data); e != nil {
			rows.Close()
			a.error(w, e)
			return
		}
		files[path] = data
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		a.error(w, e)
		return
	}
	_, e = tx.Exec(r.Context(), `INSERT INTO community_candidate_skin_downloads(skin_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, r.PathValue("id"), p.UserID)
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		a.error(w, e)
		return
	}
	write(w, 200, map[string]any{"id": r.PathValue("id"), "package_id": packageID, "manifest": string(manifest), "files": files})
}
func (a *Service) communityCandidateRate(w http.ResponseWriter, r *http.Request) {
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
	result, e := a.store.pool.Exec(r.Context(), `INSERT INTO community_candidate_skin_ratings(skin_id,user_id,stars)
 SELECT s.id,$2,$3 FROM community_candidate_skins s WHERE s.id=$1 AND s.owner_id<>$2 AND EXISTS(SELECT 1 FROM community_candidate_skin_downloads WHERE skin_id=s.id AND user_id=$2)
 ON CONFLICT(skin_id,user_id) DO UPDATE SET stars=excluded.stars`, r.PathValue("id"), p.UserID, input.Stars)
	if e != nil {
		a.error(w, e)
		return
	}
	if result.RowsAffected() == 0 {
		// A missing skin is 404; an existing one the caller owns or has not downloaded is 403.
		var exists bool
		if e = a.store.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM community_candidate_skins WHERE id=$1)`, r.PathValue("id")).Scan(&exists); e != nil {
			a.error(w, e)
			return
		}
		if !exists {
			writeError(w, 404, "skin_not_found")
			return
		}
		writeError(w, 403, "download_before_rating_or_own_skin")
		return
	}
	write(w, 200, map[string]int{"stars": input.Stars})
}
func (a *Service) communityCandidateDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r, false)
	if !ok {
		return
	}
	result, e := a.store.pool.Exec(r.Context(), `DELETE FROM community_candidate_skins WHERE id=$1 AND owner_id=$2`, r.PathValue("id"), p.UserID)
	if e != nil {
		a.error(w, e)
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, 404, "skin_not_found")
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
