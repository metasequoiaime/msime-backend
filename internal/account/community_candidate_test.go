package account

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const candidateManifestTemplate = "schema_version = 1\nid = '{id}'\nname = 'Shared'\nversion = '1.0'\nbase = 'night'\npreview = 'preview.png'\n[supports]\nlayouts = ['horizontal', 'vertical']\nthemes = ['dark', 'light']\n[candidate_window]\nmin_width_dip = 176\n[candidate_window.decoration]\nimage = 'assets/deco.jpg'\ntop_inset_dip = 40\nwidth_dip = 40\n[license]\ncode = 'MIT'\nassets = 'CC-BY-4.0'\nsource = 'synthetic'\n"

func candidatePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for i := range img.Pix {
		img.Pix[i] = byte(i * 7)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// candidateNoisePNG is incompressible, so its re-encoded size stays close to three bytes per pixel.
func candidateNoisePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	random := rand.New(rand.NewPCG(1, 2))
	for i := range img.Pix {
		img.Pix[i] = byte(random.Uint32())
		if i%4 == 3 {
			img.Pix[i] = 255
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func candidateJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// candidateFixture is a valid package: a PNG preview and a JPEG decoration, both referenced by the manifest.
func candidateFixture(t *testing.T, packageID string) (string, map[string][]byte) {
	t.Helper()
	return strings.Replace(candidateManifestTemplate, "{id}", packageID, 1), map[string][]byte{"preview.png": candidatePNG(t, 16, 12), "assets/deco.jpg": candidateJPEG(t, 20, 20)}
}

func candidatePublishBody(t *testing.T, id, name, manifest string, files map[string][]byte) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"id": id, "name": name, "description": "A shared skin", "manifest": manifest, "files": files})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// insertCandidateSkin writes one package row and its preview file directly, satisfying every CHECK constraint.
func insertCandidateSkin(t *testing.T, db *Store, id, owner, name string) {
	t.Helper()
	if _, err := db.pool.Exec(t.Context(), `INSERT INTO community_candidate_skins(id,owner_id,package_id,name,version,license_assets,manifest,preview_path,request_sha256) VALUES($1,$2,'shared',$3,'1.0','CC-BY-4.0','schema_version = 1','preview.png',$4)`, id, owner, name, hash(id)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(t.Context(), `INSERT INTO community_candidate_skin_files(skin_id,path,bytes) VALUES($1,'preview.png',$2)`, id, candidatePNG(t, 4, 4)); err != nil {
		t.Fatal(err)
	}
}

// candidatePipeline runs every pure publish check in handler order and returns the first error code.
func candidatePipeline(manifest string, files map[string][]byte) string {
	if code := validCandidateFiles(manifest, files); code != "" {
		return code
	}
	pkg, code := validCandidatePackage(manifest, files)
	if code != "" {
		return code
	}
	_, code = sanitizeCandidateImages(files, pkg.Preview)
	return code
}

// pngWithChunk inserts an ancillary chunk right after IHDR.
func pngWithChunk(data []byte, kind string, payload []byte) []byte {
	chunk := make([]byte, 8, 12+len(payload))
	binary.BigEndian.PutUint32(chunk, uint32(len(payload)))
	copy(chunk[4:], kind)
	chunk = append(chunk, payload...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(chunk[4:]))
	const ihdrEnd = 8 + 25
	return append(append(append([]byte{}, data[:ihdrEnd]...), chunk...), data[ihdrEnd:]...)
}

func TestCommunityCandidatePackageValidation(t *testing.T) {
	manifest, files := candidateFixture(t, "shared")
	if code := candidatePipeline(manifest, files); code != "" {
		t.Fatal("valid package refused", code)
	}
	pngData, jpgData := files["preview.png"], files["assets/deco.jpg"]
	with := func(extra map[string][]byte, drop ...string) map[string][]byte {
		out := map[string][]byte{"preview.png": pngData, "assets/deco.jpg": jpgData}
		for _, key := range drop {
			delete(out, key)
		}
		for key, value := range extra {
			out[key] = value
		}
		return out
	}
	edit := func(old, next string) string {
		if !strings.Contains(manifest, old) {
			t.Fatalf("fixture has no %q", old)
		}
		return strings.Replace(manifest, old, next, 1)
	}
	for _, tc := range []struct {
		name, manifest string
		files          map[string][]byte
		code           string
	}{
		{"skin.toml key", manifest, with(map[string][]byte{"skin.toml": pngData}), "candidate_skin_file_path"},
		{"parent segment", manifest, with(map[string][]byte{"../x.png": pngData}), "candidate_skin_file_path"},
		{"dot segment", manifest, with(map[string][]byte{"a/./b.png": pngData}), "candidate_skin_file_path"},
		{"backslash", manifest, with(map[string][]byte{"a\\b.png": pngData}), "candidate_skin_file_path"},
		{"absolute", manifest, with(map[string][]byte{"/abs.png": pngData}), "candidate_skin_file_path"},
		{"webp", manifest, with(map[string][]byte{"x.webp": pngData}), "candidate_skin_file_type"},
		{"css", manifest, with(map[string][]byte{"style.css": []byte("a{}")}), "candidate_skin_file_type"},
		{"svg", manifest, with(map[string][]byte{"x.svg": []byte("<svg/>")}), "candidate_skin_file_type"},
		{"gif", manifest, with(map[string][]byte{"x.gif": pngData}), "candidate_skin_file_type"},
		{"no extension", manifest, with(map[string][]byte{"noext": pngData}), "candidate_skin_file_type"},
		{"case duplicate", manifest, with(map[string][]byte{"Preview.png": pngData}), "candidate_skin_file_path"},
		{"four files", manifest, with(map[string][]byte{"a.png": pngData, "b.png": pngData}), "candidate_skin_file_path"},
		{"no files", manifest, map[string][]byte{}, "candidate_skin_file_path"},
		{"empty file", manifest, with(map[string][]byte{"preview.png": {}}), "candidate_skin_too_large"},
		{"file over 1 MiB", manifest, with(map[string][]byte{"preview.png": make([]byte, 1<<20+1)}), "candidate_skin_too_large"},
		{"total over 2 MiB", manifest, with(map[string][]byte{"preview.png": make([]byte, 1<<20), "assets/deco.jpg": make([]byte, 1<<20), "b.png": {1}}), "candidate_skin_too_large"},
		{"manifest over 64 KiB", manifest + "#" + strings.Repeat("x", 65536), files, "candidate_skin_too_large"},
		{"jpeg named png", manifest, with(map[string][]byte{"preview.png": jpgData}), "candidate_skin_image_invalid"},
		{"truncated png", manifest, with(map[string][]byte{"preview.png": pngData[:len(pngData)/2]}), "candidate_skin_image_invalid"},
		{"truncated jpeg", manifest, with(map[string][]byte{"assets/deco.jpg": jpgData[:len(jpgData)/2]}), "candidate_skin_image_invalid"},
		{"png header only", manifest, with(map[string][]byte{"preview.png": pngData[:20]}), "candidate_skin_image_invalid"},
		{"side over 2048", manifest, with(map[string][]byte{"preview.png": candidatePNG(t, 2049, 1)}), "candidate_skin_too_large"},
		{"pixels over budget", manifest, with(map[string][]byte{"preview.png": candidatePNG(t, 2000, 2000), "assets/deco.jpg": candidateJPEG(t, 2000, 2001)}), "candidate_skin_too_large"},
		{"missing license", edit("[license]\ncode = 'MIT'\nassets = 'CC-BY-4.0'\nsource = 'synthetic'\n", ""), files, "candidate_skin_license_required"},
		{"license without assets", edit("assets = 'CC-BY-4.0'\n", ""), files, "candidate_skin_license_required"},
		{"blank license assets", edit("assets = 'CC-BY-4.0'\n", "assets = '  '\n"), files, "candidate_skin_license_required"},
		{"missing preview", edit("preview = 'preview.png'\n", ""), files, "candidate_skin_preview_required"},
		{"directory preview", edit("preview = 'preview.png'\n", "preview = 'assets'\n"), files, "candidate_skin_file_type"},
		{"preview over 256 KiB", manifest, with(map[string][]byte{"preview.png": candidateNoisePNG(t, 320, 320)}), "candidate_skin_too_large"},
		{"toolbar stylesheet", edit("preview = ", "toolbar_stylesheet = 'toolbar.css'\npreview = "), with(map[string][]byte{"toolbar.css": []byte("a{}")}), "candidate_skin_file_type"},
		{"unreferenced extra", manifest, with(map[string][]byte{"extra.png": pngData}), "invalid_candidate_skin_package"},
		{"missing referenced image", manifest, with(nil, "assets/deco.jpg"), "invalid_candidate_skin_package"},
		{"directory named like an image", edit("image = 'assets/deco.jpg'", "image = 'deco.png'"), with(map[string][]byte{"deco.png/inner.png": pngData}, "assets/deco.jpg"), "invalid_candidate_skin_package"},
		{"reserved theme id", edit("id = 'shared'", "id = 'night'"), files, "invalid_candidate_skin_package"},
		{"builtin id", edit("id = 'shared'", "id = 'fluent'"), files, "invalid_candidate_skin_package"},
		{"uppercase id", edit("id = 'shared'", "id = 'Shared'"), files, "invalid_candidate_skin_package"},
		{"non-string id", edit("id = 'shared'", "id = 5"), files, "invalid_candidate_skin_package"},
		{"missing id", edit("id = 'shared'\n", ""), files, "invalid_candidate_skin_package"},
		{"invalid TOML", "schema_version = [", files, "invalid_candidate_skin_package"},
		{"wrong schema version", edit("schema_version = 1", "schema_version = 2"), files, "invalid_candidate_skin_package"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code := candidatePipeline(tc.manifest, tc.files); code != tc.code {
				t.Fatalf("got %q want %q", code, tc.code)
			}
		})
	}
	// ParseStored accepts a stylesheet that exists; the outer v1 rule still refuses it even when the structural .css check is bypassed.
	if _, code := validCandidatePackage(edit("preview = ", "toolbar_stylesheet = 'toolbar.css'\npreview = "), with(map[string][]byte{"toolbar.css": []byte("a{}")})); code != "candidate_skin_file_type" {
		t.Fatal("stylesheet accepted", code)
	}
	// A background image joins the referenced set.
	background := edit("[license]", "[candidate_window.background]\nimage = 'assets/bg.png'\n[license]")
	if code := candidatePipeline(background, with(map[string][]byte{"assets/bg.png": pngData})); code != "" {
		t.Fatal("background package refused", code)
	}
	if code := candidatePipeline(background, files); code != "invalid_candidate_skin_package" {
		t.Fatal("missing background accepted", code)
	}

	t.Run("metadata is stripped", func(t *testing.T) {
		tagged := pngWithChunk(pngWithChunk(pngData, "tEXt", []byte("Comment\x00secret-png-text")), "iTXt", []byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00secret-xmp"))
		exif := append([]byte("Exif\x00\x00"), []byte("secret-exif-data")...)
		segment := append([]byte{0xFF, 0xE1, byte((len(exif) + 2) >> 8), byte(len(exif) + 2)}, exif...)
		tagjpg := append(append(append([]byte{}, jpgData[:2]...), segment...), jpgData[2:]...)
		input := map[string][]byte{"preview.png": tagged, "assets/deco.jpg": tagjpg}
		if code := candidatePipeline(manifest, input); code != "" {
			t.Fatal("tagged images refused", code)
		}
		clean, code := sanitizeCandidateImages(input, "preview.png")
		if code != "" {
			t.Fatal(code)
		}
		for path, data := range clean {
			for _, marker := range []string{"tEXt", "iTXt", "Exif", "secret"} {
				if bytes.Contains(data, []byte(marker)) {
					t.Fatalf("%s kept %s", path, marker)
				}
			}
		}
		if _, err := png.Decode(bytes.NewReader(clean["preview.png"])); err != nil {
			t.Fatal(err)
		}
		if _, err := jpeg.Decode(bytes.NewReader(clean["assets/deco.jpg"])); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("request digest", func(t *testing.T) {
		base := candidateRequestDigest("Name", "Description", manifest, files)
		if len(base) != 64 || base != candidateRequestDigest("Name", "Description", manifest, with(nil)) {
			t.Fatal("digest is not stable", base)
		}
		changedByte := with(map[string][]byte{"preview.png": append(append([]byte{}, pngData[:len(pngData)-1]...), pngData[len(pngData)-1]^1)})
		renamed := with(map[string][]byte{"assets/other.jpg": jpgData}, "assets/deco.jpg")
		for name, other := range map[string]string{
			"name":        candidateRequestDigest("Other", "Description", manifest, files),
			"description": candidateRequestDigest("Name", "Other", manifest, files),
			"manifest":    candidateRequestDigest("Name", "Description", manifest+"\n", files),
			"byte":        candidateRequestDigest("Name", "Description", manifest, changedByte),
			"path":        candidateRequestDigest("Name", "Description", manifest, renamed),
			"boundary":    candidateRequestDigest("NameD", "escription", manifest, files),
		} {
			if other == base {
				t.Fatal("digest ignores", name)
			}
		}
	})
}

func TestCandidateImageSlot(t *testing.T) {
	for range cap(candidateImageSlots) {
		candidateImageSlots <- struct{}{}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if acquireCandidateImageSlot(cancelled) {
		t.Fatal("acquired a slot while every slot was taken")
	}
	for range cap(candidateImageSlots) {
		<-candidateImageSlots
	}
	if !acquireCandidateImageSlot(context.Background()) {
		t.Fatal("free slot not acquired")
	}
	<-candidateImageSlots
}

func TestCommunityCandidateSkinLifecycle(t *testing.T) {
	db := testStore(t)
	a := &Service{store: db}
	mux := http.NewServeMux()
	Mount(mux, a)
	owner := complete(t, db, Identity{"email", "candidate-owner@example.test"})
	other := complete(t, db, Identity{"email", "candidate-other@example.test"})
	id := "cd334455-1234-4234-8234-123456789abc"
	manifest, files := candidateFixture(t, "shared")
	body := candidatePublishBody(t, strings.ToUpper(id), "  共享皮肤  ", manifest, files)

	w := apiRequest(t, mux, "POST", "/v1/community/candidate-skins", body, owner.AccessToken, 201)
	var created CommunityCandidateSkin
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.ID != id || created.PackageID != "shared" || created.Name != "共享皮肤" || created.Version != "1.0" || created.License != (CandidateSkinLicense{"MIT", "CC-BY-4.0", "synthetic"}) || created.FileCount != 2 || created.Size <= 0 || !created.Owned || created.Downloads != 0 || created.CreatedAt.IsZero() {
		t.Fatal(w.Body.String(), err)
	}
	w = apiRequest(t, mux, "POST", "/v1/community/candidate-skins", body, owner.AccessToken, 200)
	var retried CommunityCandidateSkin
	if err := json.Unmarshal(w.Body.Bytes(), &retried); err != nil || retried != created {
		t.Fatal("retry changed the item", w.Body.String(), err)
	}
	apiRequest(t, mux, "POST", "/v1/community/candidate-skins", candidatePublishBody(t, id, "Renamed", manifest, files), owner.AccessToken, 409)
	apiRequest(t, mux, "POST", "/v1/community/candidate-skins", body, other.AccessToken, 409)

	w = apiRequest(t, mux, "GET", "/v1/community/candidate-skins", "", "", 200)
	var page struct {
		Skins []CommunityCandidateSkin `json:"skins"`
		More  bool                     `json:"has_more"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || len(page.Skins) != 1 || page.More || page.Skins[0].Owned || page.Skins[0].ID != id {
		t.Fatal(w.Body.String(), err)
	}
	for _, leak := range []string{"schema_version", "manifest", `"files"`, `"data"`} {
		if strings.Contains(w.Body.String(), leak) {
			t.Fatal("list leaked", leak)
		}
	}
	apiRequest(t, mux, "GET", "/v1/community/candidate-skins?q=共享", "", "", 200)
	w = apiRequest(t, mux, "GET", "/v1/community/candidate-skins/"+id, "", owner.AccessToken, 200)
	var detail CommunityCandidateSkin
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || detail != created {
		t.Fatal(w.Body.String(), err)
	}
	apiRequest(t, mux, "GET", "/v1/community/candidate-skins/missing", "", "", 404)
	apiRequest(t, mux, "GET", "/v1/community/candidate-skins/missing/preview", "", "", 404)

	w = apiRequest(t, mux, "GET", "/v1/community/candidate-skins/"+id+"/preview", "", "", 200)
	var preview struct {
		Path        string `json:"path"`
		ContentType string `json:"content_type"`
		Data        []byte `json:"data"`
	}
	var stored []byte
	if err := db.pool.QueryRow(t.Context(), `SELECT bytes FROM community_candidate_skin_files WHERE skin_id=$1 AND path='preview.png'`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil || preview.Path != "preview.png" || preview.ContentType != "image/png" || !bytes.Equal(preview.Data, stored) {
		t.Fatal(w.Body.String(), err)
	}

	apiRequest(t, mux, "PUT", "/v1/community/candidate-skins/"+id+"/rating", `{"stars":4}`, other.AccessToken, 403)
	apiRequest(t, mux, "PUT", "/v1/community/candidate-skins/missing/rating", `{"stars":4}`, other.AccessToken, 404)
	apiRequest(t, mux, "PUT", "/v1/community/candidate-skins/"+id+"/rating", `{"stars":6}`, other.AccessToken, 400)
	apiRequest(t, mux, "POST", "/v1/community/candidate-skins/missing/download", ``, other.AccessToken, 404)

	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("POST", "/v1/community/candidate-skins/"+id+"/download", nil)
			r.Header.Set("Authorization", "Bearer "+other.AccessToken)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			codes <- w.Code
		}()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatal("concurrent download failed", code)
		}
	}
	w = apiRequest(t, mux, "POST", "/v1/community/candidate-skins/"+id+"/download", ``, other.AccessToken, 200)
	var pkg struct {
		ID        string            `json:"id"`
		PackageID string            `json:"package_id"`
		Manifest  string            `json:"manifest"`
		Files     map[string][]byte `json:"files"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &pkg); err != nil || pkg.ID != id || pkg.PackageID != "shared" || pkg.Manifest != manifest || len(pkg.Files) != 2 {
		t.Fatal(w.Body.String(), err)
	}
	rows, err := db.pool.Query(t.Context(), `SELECT path,sha256,size FROM community_candidate_skin_files WHERE skin_id=$1`, id)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for rows.Next() {
		var path, digest string
		var size int
		if err := rows.Scan(&path, &digest, &size); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(pkg.Files[path])
		if hex.EncodeToString(sum[:]) != digest || len(pkg.Files[path]) != size {
			t.Fatal("downloaded file does not match its digest", path)
		}
		total += size
	}
	if rows.Err() != nil || int64(total) != created.Size {
		t.Fatal("size is not the stored total", total, created.Size, rows.Err())
	}
	var downloads int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM community_candidate_skin_downloads WHERE skin_id=$1`, id).Scan(&downloads); err != nil || downloads != 1 {
		t.Fatal("downloads not deduplicated", downloads, err)
	}

	apiRequest(t, mux, "PUT", "/v1/community/candidate-skins/"+id+"/rating", `{"stars":4}`, other.AccessToken, 200)
	apiRequest(t, mux, "PUT", "/v1/community/candidate-skins/"+id+"/rating", `{"stars":5}`, other.AccessToken, 200)
	apiRequest(t, mux, "POST", "/v1/community/candidate-skins/"+id+"/download", ``, owner.AccessToken, 200)
	apiRequest(t, mux, "PUT", "/v1/community/candidate-skins/"+id+"/rating", `{"stars":1}`, owner.AccessToken, 403)
	w = apiRequest(t, mux, "GET", "/v1/community/candidate-skins/"+id, "", other.AccessToken, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || detail.MyRating != 5 || detail.RatingCount != 1 || detail.RatingAverage != 5 || detail.Downloads != 2 || detail.Owned {
		t.Fatal(w.Body.String(), err)
	}

	for token, count := range map[string]int{owner.AccessToken: 1, other.AccessToken: 0} {
		w = apiRequest(t, mux, "GET", "/v1/community/candidate-skins?scope=mine", "", token, 200)
		page.Skins = nil
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || len(page.Skins) != count {
			t.Fatal(w.Body.String(), err)
		}
	}
	apiRequest(t, mux, "GET", "/v1/community/candidate-skins?scope=mine", "", "", 401)

	apiRequest(t, mux, "DELETE", "/v1/community/candidate-skins/"+id, "", other.AccessToken, 404)
	apiRequest(t, mux, "DELETE", "/v1/community/candidate-skins/"+id, "", owner.AccessToken, 200)
	apiRequest(t, mux, "GET", "/v1/community/candidate-skins/"+id, "", "", 404)
	var remaining int
	if err := db.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM community_candidate_skin_files WHERE skin_id=$1)+(SELECT count(*) FROM community_candidate_skin_downloads WHERE skin_id=$1)+(SELECT count(*) FROM community_candidate_skin_ratings WHERE skin_id=$1)`, id).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("delete left rows", remaining, err)
	}

	// Deleting the account removes the author's packages and everything that references them.
	second := "cd334455-1234-4234-8234-000000000002"
	apiRequest(t, mux, "POST", "/v1/community/candidate-skins", candidatePublishBody(t, second, "Second", manifest, files), owner.AccessToken, 201)
	apiRequest(t, mux, "POST", "/v1/community/candidate-skins/"+second+"/download", ``, other.AccessToken, 200)
	apiRequest(t, mux, "PUT", "/v1/community/candidate-skins/"+second+"/rating", `{"stars":3}`, other.AccessToken, 200)
	if err := db.DeleteUser(t.Context(), owner.User.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM community_candidate_skins)+(SELECT count(*) FROM community_candidate_skin_files)+(SELECT count(*) FROM community_candidate_skin_downloads)+(SELECT count(*) FROM community_candidate_skin_ratings)`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("account deletion left rows", remaining, err)
	}
}

func TestCommunityCandidateSkinBoundaries(t *testing.T) {
	db := testStore(t)
	a := &Service{store: db}
	mux := http.NewServeMux()
	Mount(mux, a)
	owner := complete(t, db, Identity{"email", "candidate-catalog@example.test"})
	limited := complete(t, db, Identity{"email", "candidate-limited@example.test"})
	for i := 0; i < 50; i++ {
		insertCandidateSkin(t, db, fmt.Sprintf("ab334455-1234-1234-1234-%012d", i), owner.User.ID, fmt.Sprintf("skin %02d", i))
	}
	var page struct {
		Skins []CommunityCandidateSkin `json:"skins"`
		More  bool                     `json:"has_more"`
	}
	w := apiRequest(t, mux, "GET", "/v1/community/candidate-skins", "", owner.AccessToken, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || !page.More || len(page.Skins) != 20 || !page.Skins[0].Owned || page.Skins[0].FileCount != 1 || page.Skins[0].Size <= 0 {
		t.Fatal(w.Body.String(), err)
	}
	page.Skins = nil
	w = apiRequest(t, mux, "GET", "/v1/community/candidate-skins?offset=40&scope=mine", "", owner.AccessToken, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.More || len(page.Skins) != 10 {
		t.Fatal(w.Body.String(), err)
	}
	for query, code := range map[string]string{"offset=-1": "invalid_offset", "offset=100001": "invalid_offset", "offset=bad": "invalid_offset", "q=" + strings.Repeat("x", 129): "invalid_search", "q=%ff": "invalid_search", "scope=saved": "invalid_scope"} {
		w := apiRequest(t, mux, "GET", "/v1/community/candidate-skins?"+query, "", "", 400)
		if !strings.Contains(w.Body.String(), code) {
			t.Fatal(query, w.Body.String())
		}
	}

	manifest, files := candidateFixture(t, "shared")
	valid := "cd334455-1234-4234-8234-999999999999"
	for _, tc := range []struct{ body, code string }{
		{candidatePublishBody(t, "cd334455x1234-4234-8234-999999999999", "Name", manifest, files), "invalid_community_id"},
		{candidatePublishBody(t, "zd334455-1234-4234-8234-999999999999", "Name", manifest, files), "invalid_community_id"},
		{candidatePublishBody(t, valid, "   ", manifest, files), "invalid_skin_metadata"},
		{candidatePublishBody(t, valid, strings.Repeat("名", 33), manifest, files), "invalid_skin_metadata"},
		{candidatePublishBody(t, valid, "Name", manifest, map[string][]byte{"preview.png": files["preview.png"], "x.webp": {1}}), "candidate_skin_file_type"},
		{candidatePublishBody(t, valid, "Name", strings.Replace(manifest, "assets = 'CC-BY-4.0'\n", "", 1), files), "candidate_skin_license_required"},
		{candidatePublishBody(t, valid, "Name", manifest, map[string][]byte{"preview.png": files["assets/deco.jpg"], "assets/deco.jpg": files["assets/deco.jpg"]}), "candidate_skin_image_invalid"},
		{`{"id":"` + valid + `","name":"Name","description":"","manifest":"","files":{"preview.png":"not base64!"}}`, "invalid_json"},
	} {
		w := apiRequest(t, mux, "POST", "/v1/community/candidate-skins", tc.body, limited.AccessToken, 400)
		if !strings.Contains(w.Body.String(), tc.code) {
			t.Fatal(tc.code, w.Body.String())
		}
	}
	w = apiRequest(t, mux, "POST", "/v1/community/candidate-skins", `{"id":"`+valid+`","name":"`+strings.Repeat("x", 3_200_000)+`"}`, limited.AccessToken, 400)
	if !strings.Contains(w.Body.String(), "invalid_json") {
		t.Fatal(w.Body.String())
	}

	w = apiRequest(t, mux, "POST", "/v1/community/candidate-skins", candidatePublishBody(t, valid, "Over quota", manifest, files), owner.AccessToken, 409)
	if !strings.Contains(w.Body.String(), "candidate_skin_publish_limit") {
		t.Fatal(w.Body.String())
	}

	// A full image pipeline answers 503 once the request deadline passes, before any decode work.
	for range cap(candidateImageSlots) {
		candidateImageSlots <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	r := httptest.NewRequest("POST", "/v1/community/candidate-skins", strings.NewReader(candidatePublishBody(t, valid, "Busy", manifest, files))).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+limited.AccessToken)
	busy := httptest.NewRecorder()
	a.communityCandidatePublish(busy, r)
	cancel()
	for range cap(candidateImageSlots) {
		<-candidateImageSlots
	}
	if busy.Code != 503 || !strings.Contains(busy.Body.String(), "candidate_skin_busy") {
		t.Fatal(busy.Code, busy.Body.String())
	}

	// The per-user publish rate is counted in auth_rates under the hashed account id.
	if _, err := db.pool.Exec(t.Context(), `INSERT INTO auth_rates(key,count,expires_at) VALUES($1,10,now()+interval '1 hour') ON CONFLICT(key) DO UPDATE SET count=10,expires_at=excluded.expires_at`, "candidate-publish:"+hash(limited.User.ID)); err != nil {
		t.Fatal(err)
	}
	w = apiRequest(t, mux, "POST", "/v1/community/candidate-skins", candidatePublishBody(t, valid, "Limited", manifest, files), limited.AccessToken, 429)
	if w.Header().Get("Retry-After") != "60" || !strings.Contains(w.Body.String(), "rate_limit_exceeded") {
		t.Fatal(w.Header(), w.Body.String())
	}
}

func TestCommunityCandidateSkinSchemaRejectsUnsafeRows(t *testing.T) {
	db := testStore(t)
	owner := complete(t, db, Identity{"email", "candidate-schema@example.test"})
	id := "ef334455-1234-4234-8234-123456789abc"
	insertCandidateSkin(t, db, id, owner.User.ID, "valid")
	skin := func(id, packageID, assets string) string {
		return `INSERT INTO community_candidate_skins(id,owner_id,package_id,name,version,license_assets,manifest,preview_path,request_sha256) VALUES('` + id + `',$1,'` + packageID + `','n','1','` + assets + `','m','p.png','` + strings.Repeat("a", 64) + `')`
	}
	file := func(path, size string) string {
		return `INSERT INTO community_candidate_skin_files(skin_id,path,bytes) VALUES($1,'` + path + `',decode(repeat('00',` + size + `),'hex'))`
	}
	for _, tc := range []struct{ query, arg string }{
		{skin("not-a-uuid", "shared", "CC0"), owner.User.ID},
		{skin("EF334455-1234-4234-8234-123456789ABC", "shared", "CC0"), owner.User.ID},
		{skin("ef334455-1234-4234-8234-000000000001", "night", "CC0"), owner.User.ID},
		{skin("ef334455-1234-4234-8234-000000000002", "fluent", "CC0"), owner.User.ID},
		{skin("ef334455-1234-4234-8234-000000000003", "Upper", "CC0"), owner.User.ID},
		{skin("ef334455-1234-4234-8234-000000000004", "shared", "   "), owner.User.ID},
		{skin("ef334455-1234-4234-8234-000000000005", "shared", strings.Repeat("x", 121)), owner.User.ID},
		{`INSERT INTO community_candidate_skins(id,owner_id,package_id,name,version,license_assets,manifest,preview_path,request_sha256) VALUES('ef334455-1234-4234-8234-000000000006',$1,'shared','n','1','CC0','m','p.png','forged')`, owner.User.ID},
		{`INSERT INTO community_candidate_skins(id,owner_id,package_id,name,version,license_assets,manifest,preview_path,request_sha256) VALUES('ef334455-1234-4234-8234-000000000007',$1,'shared','n','1','CC0','','p.png','` + strings.Repeat("a", 64) + `')`, owner.User.ID},
		{file("../x.png", "4"), id},
		{file("a/./b.png", "4"), id},
		{file("/abs.png", "4"), id},
		{file("a\\b.png", "4"), id},
		{file("style.css", "4"), id},
		{file("x.webp", "4"), id},
		{file("big.png", "1048577"), id},
		{file("empty.png", "0"), id},
		{file("preview.png", "4"), id},
		{`INSERT INTO community_candidate_skin_ratings(skin_id,user_id,stars) VALUES('` + id + `',$1,6)`, owner.User.ID},
	} {
		if _, err := db.pool.Exec(t.Context(), tc.query, tc.arg); err == nil {
			t.Fatal("unsafe row accepted:", tc.query[:min(len(tc.query), 160)])
		}
	}
	if _, err := db.pool.Exec(t.Context(), `INSERT INTO community_candidate_skin_files(skin_id,path,bytes) VALUES($1,'assets/Deco.JPEG',decode('00','hex'))`, id); err != nil {
		t.Fatal("safe row refused", err)
	}
}
