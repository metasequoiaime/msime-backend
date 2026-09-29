package account

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/metasequoiaime/MSIME-Backend/internal/skins"
)

// Curated candidate-window skin packages (candidate_skin_schema.sql). The store only reads rows; validation against the client dialect happens in internal/skins on every read, so a row an operator edited by hand is reported as invalid instead of being served.

// maxCandidateSkins matches the 256-entry bound skins_root has; a larger table fails the catalog like an oversized directory does.
const maxCandidateSkins = 256

// SkinDatabase returns the stored-package source for /v1/skins, or nil when accounts (and therefore the database) are disabled.
func (a *Service) SkinDatabase() skins.Database {
	if a == nil {
		return nil
	}
	return a.store
}

// candidateSkins reads published packages with their resource listings in one statement, so the manifest and the file list come from the same snapshot. An empty id reads every package.
func (s *Store) candidateSkins(ctx context.Context, id string) ([]skins.Stored, error) {
	rows, err := s.pool.Query(ctx, `SELECT k.id,k.manifest,
 coalesce(array_agg(r.path ORDER BY r.path) FILTER (WHERE r.path IS NOT NULL),'{}'),
 coalesce(array_agg(r.size ORDER BY r.path) FILTER (WHERE r.path IS NOT NULL),'{}'),
 coalesce(array_agg(r.sha256 ORDER BY r.path) FILTER (WHERE r.path IS NOT NULL),'{}')
 FROM candidate_skins k LEFT JOIN candidate_skin_resources r ON r.skin_id=k.id
 WHERE k.published AND ($1='' OR k.id=$1) GROUP BY k.id ORDER BY k.id LIMIT $2`, id, maxCandidateSkins+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []skins.Stored{}
	for rows.Next() {
		var p skins.Stored
		var paths, digests []string
		var sizes []int32
		if err = rows.Scan(&p.ID, &p.Manifest, &paths, &sizes, &digests); err != nil {
			return nil, err
		}
		for i := range paths {
			p.Resources = append(p.Resources, skins.StoredResource{Path: paths[i], Size: int(sizes[i]), SHA256: digests[i]})
		}
		out = append(out, p)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > maxCandidateSkins {
		return nil, skins.ErrInvalid
	}
	return out, nil
}

func (s *Store) CandidateSkins(ctx context.Context) ([]skins.Stored, error) {
	return s.candidateSkins(ctx, "")
}

func (s *Store) CandidateSkin(ctx context.Context, id string) (skins.Stored, error) {
	found, err := s.candidateSkins(ctx, id)
	if err != nil {
		return skins.Stored{}, err
	}
	if len(found) == 0 {
		return skins.Stored{}, skins.ErrNotFound
	}
	return found[0], nil
}

func (s *Store) CandidateSkinResource(ctx context.Context, id, path string) ([]byte, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT r.bytes FROM candidate_skin_resources r JOIN candidate_skins k ON k.id=r.skin_id
 WHERE k.published AND r.skin_id=$1 AND r.path=$2`, id, path).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, skins.ErrNotFound
	}
	return raw, err
}
