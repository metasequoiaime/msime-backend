package account

import (
	"context"
	"time"
)

// GitHub Release download snapshots (unit U7 writes them; unit U6 reads them for the downloads summary).

// ReleaseAssetSnapshot is one asset's cumulative GitHub download_count on one UTC day.
type ReleaseAssetSnapshot struct {
	Repo          string    `json:"repo"`
	Tag           string    `json:"tag"`
	Asset         string    `json:"asset"`
	Day           time.Time `json:"day"`
	DownloadCount int64     `json:"download_count"`
}

// RecordReleaseAssetSnapshots upserts the day's snapshots.
func (a *Service) RecordReleaseAssetSnapshots(ctx context.Context, snapshots []ReleaseAssetSnapshot) error {
	return errNotImplemented
}
