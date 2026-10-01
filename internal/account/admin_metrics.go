package account

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Upstream service monitoring storage (unit U10): metric buckets, daily availability and incidents. The collectors and the /api/status and /api/cloud handlers are in the server package.

// actionOpenIncident opens an incident from value {service, title, description}.
func actionOpenIncident(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	return actionResult{}, errNotImplemented
}

// actionResolveIncident resolves the incident id.
func actionResolveIncident(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	return actionResult{}, errNotImplemented
}

// actionUpdateIncident changes the title or description of the incident id from value.
func actionUpdateIncident(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	return actionResult{}, errNotImplemented
}
