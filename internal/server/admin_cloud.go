package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/metasequoiaime/MSIME-Backend/internal/account"
)

// Cloud usage (unit U10).

// cloudHours is the length of each service's hourly call series.
const cloudHours = 24

// adminCloud serves GET /api/cloud; it requires view_cloud_usage. Each service gets its last 24 hours (calls, error rate, P95 and an hourly series) and this UTC month's usage against the admin.services quota, with a cost estimate from the configured unit price. Only counts, latencies and errors are recorded, never request content.
func (s *Server) adminCloud(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, 405, "method_not_allowed")
		return
	}
	if !requirePerm(w, r, account.PermViewCloudUsage) {
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)
	since := currentHour.Add(-(cloudHours - 1) * time.Hour)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	earliest := since
	if monthStart.Before(earliest) {
		earliest = monthStart
	}
	hourly, err := s.accounts.ServiceMetrics(ctx, earliest)
	if err != nil {
		slog.Error("admin cloud failed", "reason", err.Error())
		fail(w, 503, "auth_unavailable")
		return
	}
	// Buckets not flushed yet (at most a minute old) are added so the page is current.
	hourly = append(hourly, s.metrics.pending()...)
	type hourJSON struct {
		Hour   time.Time `json:"hour"`
		Calls  int64     `json:"calls"`
		Errors int64     `json:"errors"`
	}
	type quotaJSON struct {
		Limit float64 `json:"limit"`
		Unit  string  `json:"unit"`
		Used  float64 `json:"used"`
		Pct   float64 `json:"pct"`
	}
	type monthJSON struct {
		Calls  int64   `json:"calls"`
		Errors int64   `json:"errors"`
		Usage  float64 `json:"usage"`
		Meter  string  `json:"meter"`
	}
	type serviceJSON struct {
		Key       string     `json:"key"`
		Name      string     `json:"name"`
		Provider  string     `json:"provider"`
		State     string     `json:"state"`
		Calls     int64      `json:"calls_24h"`
		Errors    int64      `json:"errors_24h"`
		ErrorRate *float64   `json:"error_rate"`
		P95MS     *int       `json:"p95_ms"`
		SlowMS    int        `json:"slow_ms"`
		Hourly    []hourJSON `json:"hourly"`
		Month     monthJSON  `json:"month"`
		Quota     *quotaJSON `json:"quota"`
		CostCNY   *float64   `json:"cost_cny"`
	}
	type serviceTotals struct {
		day   metricCounts
		month metricCounts
		hours [cloudHours]metricCounts
	}
	totals := map[string]*serviceTotals{}
	for _, row := range hourly {
		t := totals[row.Service]
		if t == nil {
			t = &serviceTotals{}
			totals[row.Service] = t
		}
		counts := rowCounts(row)
		if !row.Hour.Before(monthStart) {
			t.month.add(counts)
		}
		if i := int(row.Hour.Sub(since) / time.Hour); !row.Hour.Before(since) && i < cloudHours {
			t.day.add(counts)
			t.hours[i].add(counts)
		}
	}
	_, states, err := s.statusStates(ctx)
	if err != nil {
		slog.Error("admin cloud failed", "reason", err.Error())
		fail(w, 503, "auth_unavailable")
		return
	}
	services := s.monitoredServices()
	out := make([]serviceJSON, 0, len(services))
	for _, svc := range services {
		t := totals[svc.Key]
		if t == nil {
			t = &serviceTotals{}
		}
		row := serviceJSON{Key: svc.Key, Name: svc.Name, Provider: svc.Provider, State: stateUnknown, Calls: t.day.calls, Errors: t.day.errors, SlowMS: svc.SlowMS, Hourly: make([]hourJSON, cloudHours)}
		if p, ok := states[svc.Key]; ok {
			row.State = p.state
		}
		if t.day.calls > 0 {
			rate := float64(t.day.errors) / float64(t.day.calls)
			row.ErrorRate = &rate
		}
		if p95, ok := t.day.latency.percentile(0.95); ok {
			row.P95MS = &p95
		}
		for i := range cloudHours {
			row.Hourly[i] = hourJSON{Hour: since.Add(time.Duration(i) * time.Hour), Calls: t.hours[i].calls, Errors: t.hours[i].errors}
		}
		meter := serviceMeter(svc.Key)
		row.Month = monthJSON{Calls: t.month.calls, Errors: t.month.errors, Usage: t.month.usage, Meter: meter}
		metered := meteredUnits(meter, t.month)
		if svc.UnitPrice > 0 {
			cost := metered * svc.UnitPrice
			row.CostCNY = &cost
		}
		if svc.QuotaLimit > 0 {
			used := quotaUsed(svc.QuotaUnit, meter, t.month, metered*svc.UnitPrice)
			row.Quota = &quotaJSON{Limit: svc.QuotaLimit, Unit: svc.QuotaUnit, Used: used, Pct: used / svc.QuotaLimit * 100}
		}
		out = append(out, row)
	}
	respond(w, 200, map[string]any{"generated_at": now, "since": since, "month_start": monthStart, "services": out})
}

// meteredUnits is the month's usage in the unit the service's price applies to: calls, characters or hours.
func meteredUnits(meter string, month metricCounts) float64 {
	switch meter {
	case meterChars:
		return month.usage
	case meterSeconds:
		return month.usage / 3600
	}
	return float64(month.calls)
}

// quotaUsed is the month's usage in the quota's unit; a unit the service does not meter (for example chars on a call-metered service) counts as nothing used.
func quotaUsed(unit, meter string, month metricCounts, cost float64) float64 {
	switch unit {
	case "calls":
		return float64(month.calls)
	case "chars":
		if meter == meterChars {
			return month.usage
		}
	case "hours":
		if meter == meterSeconds {
			return month.usage / 3600
		}
	case "cny":
		return cost
	}
	return 0
}
