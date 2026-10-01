package account

import "context"

// Global search (unit U11): the database half of GET /api/search; the server package adds the GitHub results it caches.

// AdminSearchHit is one search result. Target is the console page to open and Where says where the hit was found.
type AdminSearchHit struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Title  string `json:"title"`
	Where  string `json:"where"`
	Target string `json:"target"`
}

// AdminSearch searches users, community content, crash groups, sensitive words and notices for q and returns at most limit hits.
func (a *Service) AdminSearch(ctx context.Context, q string, limit int) ([]AdminSearchHit, error) {
	return nil, errNotImplemented
}
