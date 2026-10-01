package account

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Upstream service monitoring storage (unit U10): metric buckets, daily availability and incidents. The collectors and the /api/status and /api/cloud handlers are in the server package.

// serviceKeyPattern is the admin.services key format; "database" is the status page's own probe.
var serviceKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Retention of the monitoring tables: the status page shows 60 days, the cloud page the current month and the last 24 hours.
const (
	serviceDailyRetentionDays   = 60
	serviceMetricsRetentionDays = 90
)

// ServiceMetric is one service's upstream calls in one UTC hour. It never carries request or response content.
type ServiceMetric struct {
	Service string
	Hour    time.Time
	Calls   int64
	Errors  int64
	// Latency counts calls per latency bucket, keyed by the bucket's upper bound in milliseconds ("inf" for the overflow bucket).
	Latency map[string]int64
	// Usage is the metered quantity of the successful calls: characters or seconds, or 0 for services metered by calls.
	Usage float64
}

// RecordServiceMetrics adds the given hourly deltas to admin_service_metrics in one transaction, summing counts and latency buckets into existing rows.
func (a *Service) RecordServiceMetrics(ctx context.Context, rows []ServiceMetric) error {
	if len(rows) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, row := range rows {
		if !serviceKeyPattern.MatchString(row.Service) || row.Calls < 0 || row.Errors < 0 || row.Usage < 0 {
			return errors.New("invalid service metric")
		}
		latency := []byte("{}")
		if row.Latency != nil {
			var err error
			if latency, err = json.Marshal(row.Latency); err != nil {
				return err
			}
		}
		batch.Queue(`INSERT INTO admin_service_metrics AS m(service,hour,calls,errors,latency_buckets,usage) VALUES($1,date_trunc('hour',$2::timestamptz,'UTC'),$3,$4,$5::jsonb,$6)
ON CONFLICT(service,hour) DO UPDATE SET calls=m.calls+EXCLUDED.calls, errors=m.errors+EXCLUDED.errors, usage=m.usage+EXCLUDED.usage,
 latency_buckets=(SELECT COALESCE(jsonb_object_agg(k,COALESCE((m.latency_buckets->>k)::bigint,0)+COALESCE((EXCLUDED.latency_buckets->>k)::bigint,0)),'{}'::jsonb)
  FROM (SELECT jsonb_object_keys(m.latency_buckets) UNION SELECT jsonb_object_keys(EXCLUDED.latency_buckets)) AS keys(k))`,
			row.Service, row.Hour.UTC(), row.Calls, row.Errors, string(latency), row.Usage)
	}
	tx, err := a.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = tx.SendBatch(ctx, batch).Close(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ServiceMetrics returns the hourly rows from since onwards, oldest first.
func (a *Service) ServiceMetrics(ctx context.Context, since time.Time) ([]ServiceMetric, error) {
	rows, err := a.store.pool.Query(ctx, `SELECT service,hour,calls,errors,latency_buckets,usage::float8 FROM admin_service_metrics WHERE hour>=$1 ORDER BY hour,service`, since.UTC())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ServiceMetric, error) {
		var m ServiceMetric
		var latency []byte
		if err := row.Scan(&m.Service, &m.Hour, &m.Calls, &m.Errors, &latency, &m.Usage); err != nil {
			return m, err
		}
		m.Hour = m.Hour.UTC()
		return m, json.Unmarshal(latency, &m.Latency)
	})
}

// ServiceUsage is one service's totals over a period.
type ServiceUsage struct {
	Calls  int64
	Errors int64
	Usage  float64
}

// ServiceUsageSince sums each service's calls, errors and usage from since onwards.
func (a *Service) ServiceUsageSince(ctx context.Context, since time.Time) (map[string]ServiceUsage, error) {
	rows, err := a.store.pool.Query(ctx, `SELECT service,sum(calls)::bigint,sum(errors)::bigint,sum(usage)::float8 FROM admin_service_metrics WHERE hour>=$1 GROUP BY service`, since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	usage := map[string]ServiceUsage{}
	for rows.Next() {
		var service string
		var u ServiceUsage
		if err = rows.Scan(&service, &u.Calls, &u.Errors, &u.Usage); err != nil {
			return nil, err
		}
		usage[service] = u
	}
	return usage, rows.Err()
}

// ServiceProbe is one service's state in one probe minute.
type ServiceProbe struct {
	Service string
	// Available is false when the service was down in this minute.
	Available bool
	// Degraded marks a minute in which the service was slow or failing.
	Degraded bool
	// P95MS is the service's P95 latency so far today; nil keeps the stored value.
	P95MS *int
}

// RecordServiceProbes rolls one probe minute into admin_service_daily for the UTC day of at. Minutes are capped at a day's 1440 so several server instances cannot push a day past full.
func (a *Service) RecordServiceProbes(ctx context.Context, at time.Time, probes []ServiceProbe) error {
	if len(probes) == 0 {
		return nil
	}
	day := at.UTC().Format(time.DateOnly)
	batch := &pgx.Batch{}
	for _, p := range probes {
		if !serviceKeyPattern.MatchString(p.Service) {
			return errors.New("invalid service key")
		}
		ok := 0
		if p.Available {
			ok = 1
		}
		batch.Queue(`INSERT INTO admin_service_daily AS d(service,day,ok_minutes,total_minutes,degraded,p95_ms) VALUES($1,$2::date,$3,1,$4,$5)
ON CONFLICT(service,day) DO UPDATE SET total_minutes=LEAST(d.total_minutes+1,1440), ok_minutes=LEAST(d.ok_minutes+EXCLUDED.ok_minutes,d.total_minutes+1,1440),
 degraded=d.degraded OR EXCLUDED.degraded, p95_ms=COALESCE(EXCLUDED.p95_ms,d.p95_ms)`, p.Service, day, ok, p.Degraded, p.P95MS)
	}
	return a.store.pool.SendBatch(ctx, batch).Close()
}

// ServiceDay is one service's availability on one UTC day.
type ServiceDay struct {
	Service      string
	Day          time.Time
	OKMinutes    int
	TotalMinutes int
	Degraded     bool
	P95MS        *int
}

// ServiceDays returns the daily rollups from the UTC day of since onwards.
func (a *Service) ServiceDays(ctx context.Context, since time.Time) ([]ServiceDay, error) {
	rows, err := a.store.pool.Query(ctx, `SELECT service,day,ok_minutes,total_minutes,degraded,p95_ms FROM admin_service_daily WHERE day>=$1::date ORDER BY day,service`, since.UTC().Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ServiceDay, error) {
		var d ServiceDay
		err := row.Scan(&d.Service, &d.Day, &d.OKMinutes, &d.TotalMinutes, &d.Degraded, &d.P95MS)
		return d, err
	})
}

// Incident is one service incident, opened automatically by the status probe or by an admin.
type Incident struct {
	ID          int64      `json:"id"`
	Service     string     `json:"service"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	State       string     `json:"state"`
	StartedAt   time.Time  `json:"started_at"`
	ResolvedAt  *time.Time `json:"resolved_at"`
	Auto        bool       `json:"auto"`
}

// Incidents returns the open incidents and then the most recent resolved ones, at most limit in all.
func (a *Service) Incidents(ctx context.Context, limit int) ([]Incident, error) {
	rows, err := a.store.pool.Query(ctx, `SELECT id,service,title,description,state,started_at,resolved_at,auto FROM admin_incidents ORDER BY state='open' DESC,started_at DESC,id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Incident, error) {
		var i Incident
		err := row.Scan(&i.ID, &i.Service, &i.Title, &i.Description, &i.State, &i.StartedAt, &i.ResolvedAt, &i.Auto)
		return i, err
	})
}

// OpenAutoIncident opens the probe's incident for service unless one is already open, notifying the console in the same transaction. It reports whether a new incident was opened.
func (a *Service) OpenAutoIncident(ctx context.Context, service, title, description string) (bool, error) {
	if !serviceKeyPattern.MatchString(service) || !resourceText(title, 1, 200, false) || !resourceText(description, 0, 5000, true) {
		return false, ErrInvalid
	}
	tx, err := a.store.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO admin_incidents(service,title,description,auto) VALUES($1,$2,$3,true) ON CONFLICT(service) WHERE state='open' AND auto DO NOTHING RETURNING id`, service, title, description).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = a.Notify(ctx, tx, Notification{Kind: NotifyIncident, Title: title, TargetPage: "status", TargetID: strconv.FormatInt(id, 10)}); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// ResolveAutoIncident resolves the probe's open incident for service and reports whether there was one.
func (a *Service) ResolveAutoIncident(ctx context.Context, service string) (bool, error) {
	tag, err := a.store.pool.Exec(ctx, `UPDATE admin_incidents SET state='resolved',resolved_at=now() WHERE service=$1 AND state='open' AND auto`, service)
	return tag.RowsAffected() > 0, err
}

// PruneServiceMonitoring drops daily rollups older than the status page's 60 days and hourly metrics older than 90 days.
func (a *Service) PruneServiceMonitoring(ctx context.Context) error {
	if _, err := a.store.pool.Exec(ctx, `DELETE FROM admin_service_daily WHERE day<(now() AT TIME ZONE 'UTC')::date-$1::int`, serviceDailyRetentionDays-1); err != nil {
		return err
	}
	_, err := a.store.pool.Exec(ctx, `DELETE FROM admin_service_metrics WHERE hour<now()-make_interval(days=>$1::int)`, serviceMetricsRetentionDays)
	return err
}

// PingDatabase checks that the database answers a query, for the status probe.
func (a *Service) PingDatabase(ctx context.Context) error {
	var one int
	return a.store.pool.QueryRow(ctx, `SELECT 1`).Scan(&one)
}

// incidentValue is the value of the incident actions; absent fields stay nil.
type incidentValue struct {
	Service     *string `json:"service"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
}

func decodeIncidentValue(raw json.RawMessage) (incidentValue, error) {
	var v incidentValue
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) == 0 || d.Decode(&v) != nil || d.More() {
		return v, actionFail(400, "invalid_value")
	}
	if v.Title != nil && !resourceText(*v.Title, 1, 200, false) {
		return v, actionFail(400, "invalid_title")
	}
	if v.Description != nil && !resourceText(*v.Description, 0, 5000, true) {
		return v, actionFail(400, "invalid_description")
	}
	return v, nil
}

func incidentID(v actionRequest) (int64, error) {
	if err := requireActionID(v); err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(v.ID, 10, 64)
	if err != nil || id < 1 {
		return 0, actionFail(400, "invalid_id")
	}
	return id, nil
}

// actionOpenIncident opens an incident from value {service, title, description}.
func actionOpenIncident(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	value, err := decodeIncidentValue(v.Value)
	if err != nil {
		return actionResult{}, err
	}
	if value.Service == nil || !serviceKeyPattern.MatchString(*value.Service) {
		return actionResult{}, actionFail(400, "invalid_service")
	}
	if value.Title == nil {
		return actionResult{}, actionFail(400, "invalid_title")
	}
	description := ""
	if value.Description != nil {
		description = *value.Description
	}
	var id int64
	if err = tx.QueryRow(ctx, `INSERT INTO admin_incidents(service,title,description) VALUES($1,$2,$3) RETURNING id`, *value.Service, *value.Title, description).Scan(&id); err != nil {
		return actionResult{}, err
	}
	target := strconv.FormatInt(id, 10)
	if err = a.Notify(ctx, tx, Notification{Kind: NotifyIncident, Title: *value.Title, TargetPage: "status", TargetID: target}); err != nil {
		return actionResult{}, err
	}
	return actionResult{Affected: 1, Target: target, Detail: map[string]any{"service": *value.Service, "title": *value.Title}, Extra: map[string]any{"id": id}}, nil
}

// actionResolveIncident resolves the incident id.
func actionResolveIncident(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	id, err := incidentID(v)
	if err != nil {
		return actionResult{}, err
	}
	var service, title, state string
	err = tx.QueryRow(ctx, `SELECT service,title,state FROM admin_incidents WHERE id=$1 FOR UPDATE`, id).Scan(&service, &title, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return actionResult{}, actionFail(404, "not_found")
	}
	if err != nil {
		return actionResult{}, err
	}
	if state == "resolved" {
		return actionResult{}, actionFail(409, "already_resolved")
	}
	if _, err = tx.Exec(ctx, `UPDATE admin_incidents SET state='resolved',resolved_at=now() WHERE id=$1`, id); err != nil {
		return actionResult{}, err
	}
	detail := map[string]any{"service": service, "title": title}
	if v.Reason != "" {
		detail["reason"] = v.Reason
	}
	return actionResult{Affected: 1, Detail: detail}, nil
}

// actionUpdateIncident changes the title or description of the incident id from value.
func actionUpdateIncident(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	id, err := incidentID(v)
	if err != nil {
		return actionResult{}, err
	}
	value, err := decodeIncidentValue(v.Value)
	if err != nil {
		return actionResult{}, err
	}
	if value.Service != nil || (value.Title == nil && value.Description == nil) {
		return actionResult{}, actionFail(400, "invalid_value")
	}
	var title string
	err = tx.QueryRow(ctx, `UPDATE admin_incidents SET title=COALESCE($2,title),description=COALESCE($3,description) WHERE id=$1 RETURNING title`, id, value.Title, value.Description).Scan(&title)
	if errors.Is(err, pgx.ErrNoRows) {
		return actionResult{}, actionFail(404, "not_found")
	}
	if err != nil {
		return actionResult{}, err
	}
	fields := []string{}
	if value.Title != nil {
		fields = append(fields, "title")
	}
	if value.Description != nil {
		fields = append(fields, "description")
	}
	return actionResult{Affected: 1, Detail: map[string]any{"title": title, "fields": fields}}, nil
}
