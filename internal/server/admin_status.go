package server

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/metasequoiaime/MSIME-Backend/internal/account"
)

// System status (unit U10).

const (
	// statusProbeInterval is how often statusProbeJob runs.
	statusProbeInterval = time.Minute
	// statusWindowMinutes is the window of recent calls a probe judges a service by.
	statusWindowMinutes = 5
	// statusStale is how old the last probe may be before adminHealth reports unknown.
	statusStale = 3 * statusProbeInterval
	// statusDays is the length of the availability strip on the status page.
	statusDays = 60
	// databaseService is the status page's own database probe; it is recorded like an upstream service.
	databaseService = "database"
	// databaseSlowMS is the probe latency above which the database counts as degraded.
	databaseSlowMS = 1000
	// A service is degraded when more than degradedErrorRate of at least minErrorsForRate calls failed, or when its P95 is above its slow_ms with at least minCallsForP95 calls in the window; it is down when at least minCallsForDown calls all failed.
	degradedErrorRate = 0.05
	minErrorsForRate  = 2
	minCallsForP95    = 3
	minCallsForDown   = 3
	// An automatic incident opens after incidentOpenAfter bad probes in a row and resolves after incidentResolveAfter good ones.
	incidentOpenAfter    = 3
	incidentResolveAfter = 5
	statusIncidentLimit  = 20
)

// Service states. idle means no calls in the window, which counts as available.
const (
	stateOK       = "ok"
	stateIdle     = "idle"
	stateDegraded = "degraded"
	stateDown     = "down"
	stateUnknown  = "unknown"
)

// probeResult is one service's state in the latest probe.
type probeResult struct {
	state  string
	calls  int64
	errors int64
	p95    *int
	// badRuns and goodRuns count consecutive probes, for opening and resolving automatic incidents; incident is whether this process believes an automatic incident is open (initially true, so the first good streak after a restart also resolves one left open).
	badRuns, goodRuns int
	incident          bool
}

// statusSnapshot is the latest probe, kept in serviceMetrics.
type statusSnapshot struct {
	checked  time.Time
	services map[string]*probeResult
	pruned   time.Time
}

// adminStatus serves GET /api/status: the latest probe, each service's 60-day availability and the recent incidents.
func (s *Server) adminStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, 405, "method_not_allowed")
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()
	today := now.Truncate(24 * time.Hour)
	first := today.AddDate(0, 0, -(statusDays - 1))
	days, err := s.accounts.ServiceDays(ctx, first)
	if err != nil {
		slog.Error("admin status failed", "reason", err.Error())
		fail(w, 503, "auth_unavailable")
		return
	}
	incidents, err := s.accounts.Incidents(ctx, statusIncidentLimit)
	if err != nil {
		slog.Error("admin status failed", "reason", err.Error())
		fail(w, 503, "auth_unavailable")
		return
	}
	byService := map[string]map[string]account.ServiceDay{}
	for _, d := range days {
		if byService[d.Service] == nil {
			byService[d.Service] = map[string]account.ServiceDay{}
		}
		byService[d.Service][d.Day.UTC().Format(time.DateOnly)] = d
	}
	checked, states := s.statusStates()
	type dayJSON struct {
		Day    string   `json:"day"`
		State  string   `json:"state"`
		Uptime *float64 `json:"uptime"`
	}
	type serviceJSON struct {
		Key       string    `json:"key"`
		Name      string    `json:"name"`
		Desc      string    `json:"desc"`
		State     string    `json:"state"`
		P95MS     *int      `json:"p95_ms"`
		Uptime60d *float64  `json:"uptime_60d"`
		Days      []dayJSON `json:"days"`
	}
	type incidentJSON struct {
		account.Incident
		ServiceName string `json:"service_name"`
	}
	services := append([]monitoredService{{Key: databaseService, Name: "数据库", Provider: "PostgreSQL", SlowMS: databaseSlowMS}}, s.monitoredServices()...)
	names := map[string]string{}
	out := make([]serviceJSON, 0, len(services))
	for _, svc := range services {
		names[svc.Key] = svc.Name
		row := serviceJSON{Key: svc.Key, Name: svc.Name, Desc: svc.Provider, State: stateUnknown, Days: make([]dayJSON, 0, statusDays)}
		if p, ok := states[svc.Key]; ok {
			row.State = p.state
		}
		var ok, total int
		for i := range statusDays {
			day := first.AddDate(0, 0, i).Format(time.DateOnly)
			entry := dayJSON{Day: day, State: "none"}
			if d, found := byService[svc.Key][day]; found && d.TotalMinutes > 0 {
				ok += d.OKMinutes
				total += d.TotalMinutes
				uptime := float64(d.OKMinutes) / float64(d.TotalMinutes)
				entry.Uptime = &uptime
				switch {
				case d.OKMinutes < d.TotalMinutes:
					entry.State = stateDown
				case d.Degraded:
					entry.State = stateDegraded
				default:
					entry.State = stateOK
				}
				if day == today.Format(time.DateOnly) {
					row.P95MS = d.P95MS
				}
			}
			row.Days = append(row.Days, entry)
		}
		if total > 0 {
			uptime := float64(ok) / float64(total)
			row.Uptime60d = &uptime
		}
		out = append(out, row)
	}
	list := make([]incidentJSON, 0, len(incidents))
	for _, incident := range incidents {
		name := names[incident.Service]
		if name == "" {
			name = incident.Service
		}
		list = append(list, incidentJSON{incident, name})
	}
	var checkedAt *time.Time
	if !checked.IsZero() {
		checkedAt = &checked
	}
	respond(w, 200, map[string]any{"checked_at": checkedAt, "state": s.adminHealth(ctx), "services": out, "incidents": list})
}

// statusStates copies the latest probe.
func (s *Server) statusStates() (time.Time, map[string]probeResult) {
	s.metrics.mu.Lock()
	defer s.metrics.mu.Unlock()
	states := make(map[string]probeResult, len(s.metrics.status.services))
	for key, p := range s.metrics.status.services {
		states[key] = *p
	}
	return s.metrics.status.checked, states
}

// adminHealth is the overall state for the shell: ok, degraded or down, or unknown while no probe has run or the last one is stale. The database being down is down; any upstream service degraded or down is degraded.
func (s *Server) adminHealth(ctx context.Context) string {
	checked, states := s.statusStates()
	if checked.IsZero() || time.Since(checked) > statusStale {
		return stateUnknown
	}
	if db, ok := states[databaseService]; ok && db.state == stateDown {
		return stateDown
	}
	for _, p := range states {
		if p.state == stateDown || p.state == stateDegraded {
			return stateDegraded
		}
	}
	return stateOK
}

// statusProbeJob probes the database and the recent upstream metrics every minute until ctx ends, rolling the results into admin_service_daily and opening or resolving automatic incidents. It also flushes the metric buckets, and flushes them once more on the way out.
func (s *Server) statusProbeJob(ctx context.Context) {
	if s.accounts == nil {
		return
	}
	ticker := time.NewTicker(statusProbeInterval)
	defer ticker.Stop()
	s.statusTick(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := s.flushMetrics(flushCtx); err != nil {
				slog.Warn("final metrics flush failed", "reason", err.Error())
			}
			cancel()
			return
		case now := <-ticker.C:
			s.statusTick(ctx, now)
		}
	}
}

// statusTick is one probe: flush the metric buckets, ping the database, judge every service by its last five minutes, roll the minute into the daily table and open or resolve automatic incidents.
func (s *Server) statusTick(ctx context.Context, now time.Time) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	logFailure := func(what string, err error) {
		if err != nil && ctx.Err() == nil {
			slog.Warn("status probe: "+what+" failed", "reason", err.Error())
		}
	}
	logFailure("metrics flush", s.flushMetrics(ctx))
	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	started := time.Now()
	pingErr := s.accounts.PingDatabase(pingCtx)
	pingCancel()
	if ctx.Err() != nil {
		return
	}
	s.observeCall(databaseService, time.Now(), time.Since(started), pingErr != nil, 0)
	now = now.UTC()
	dayStart := now.Truncate(24 * time.Hour)
	// Today's P95 per service from the stored hours plus what is still in memory (the database probe's own call above).
	today := map[string]*metricCounts{}
	rows, err := s.accounts.ServiceMetrics(ctx, dayStart)
	logFailure("daily metrics read", err)
	for _, row := range append(rows, s.metrics.pending()...) {
		if row.Hour.Before(dayStart) {
			continue
		}
		if today[row.Service] == nil {
			today[row.Service] = &metricCounts{}
		}
		today[row.Service].add(rowCounts(row))
	}
	services := append([]monitoredService{{Key: databaseService, Name: "数据库", SlowMS: databaseSlowMS}}, s.monitoredServices()...)
	probes := make([]account.ServiceProbe, 0, len(services))
	results := make(map[string]probeResult, len(services))
	for _, svc := range services {
		window := s.metrics.recent(svc.Key, now, statusWindowMinutes)
		state := judgeService(window, svc.SlowMS)
		if svc.Key == databaseService && pingErr != nil {
			state = stateDown
		}
		result := probeResult{state: state, calls: window.calls, errors: window.errors}
		if p95, ok := window.latency.percentile(0.95); ok {
			result.p95 = &p95
		}
		probe := account.ServiceProbe{Service: svc.Key, Available: state != stateDown, Degraded: state == stateDegraded || state == stateDown}
		if c := today[svc.Key]; c != nil {
			if p95, ok := c.latency.percentile(0.95); ok {
				probe.P95MS = &p95
			}
		}
		probes = append(probes, probe)
		results[svc.Key] = result
	}
	logFailure("daily rollup", s.accounts.RecordServiceProbes(ctx, now, probes))
	s.updateIncidents(ctx, services, results, logFailure)
	s.metrics.mu.Lock()
	pruneDue := now.Sub(s.metrics.status.pruned) >= time.Hour
	s.metrics.mu.Unlock()
	if pruneDue {
		if err := s.accounts.PruneServiceMonitoring(ctx); err != nil {
			logFailure("retention", err)
		} else {
			s.metrics.mu.Lock()
			s.metrics.status.pruned = now
			s.metrics.mu.Unlock()
		}
	}
}

// updateIncidents advances each service's good and bad streaks, opens an automatic incident after incidentOpenAfter bad probes and resolves it after incidentResolveAfter good ones, then stores the probe as the latest status.
func (s *Server) updateIncidents(ctx context.Context, services []monitoredService, results map[string]probeResult, logFailure func(string, error)) {
	s.metrics.mu.Lock()
	previous := s.metrics.status.services
	s.metrics.mu.Unlock()
	next := make(map[string]*probeResult, len(results))
	for _, svc := range services {
		result := results[svc.Key]
		result.incident = true
		if p := previous[svc.Key]; p != nil {
			result.badRuns, result.goodRuns, result.incident = p.badRuns, p.goodRuns, p.incident
		}
		if result.state == stateDegraded || result.state == stateDown {
			result.badRuns++
			result.goodRuns = 0
			if result.badRuns >= incidentOpenAfter && (result.badRuns == incidentOpenAfter || !result.incident) {
				title, description := incidentText(svc, result)
				_, err := s.accounts.OpenAutoIncident(ctx, svc.Key, title, description)
				logFailure("incident open", err)
				result.incident = err == nil
			}
		} else {
			result.goodRuns++
			result.badRuns = 0
			if result.goodRuns >= incidentResolveAfter && result.incident {
				_, err := s.accounts.ResolveAutoIncident(ctx, svc.Key)
				logFailure("incident resolve", err)
				result.incident = err != nil
			}
		}
		next[svc.Key] = &result
	}
	s.metrics.mu.Lock()
	s.metrics.status.services = next
	s.metrics.status.checked = time.Now().UTC()
	s.metrics.mu.Unlock()
}

// judgeService classifies a service by its calls in the probe window.
func judgeService(window metricCounts, slowMS int) string {
	if window.calls == 0 {
		return stateIdle
	}
	if window.calls >= minCallsForDown && window.errors == window.calls {
		return stateDown
	}
	if window.errors >= minErrorsForRate && float64(window.errors)/float64(window.calls) > degradedErrorRate {
		return stateDegraded
	}
	if p95, ok := window.latency.percentile(0.95); ok && window.calls >= minCallsForP95 && p95 > slowMS {
		return stateDegraded
	}
	return stateOK
}

// incidentText phrases an automatic incident from the probe that opened it.
func incidentText(svc monitoredService, result probeResult) (string, string) {
	if svc.Key == databaseService && result.state == stateDown && result.calls == result.errors {
		return "数据库连接失败", "状态检查连续 3 分钟无法连接数据库。"
	}
	rate := 0.0
	if result.calls > 0 {
		rate = float64(result.errors) / float64(result.calls) * 100
	}
	p95 := "—"
	if result.p95 != nil {
		p95 = formatLatency(*result.p95)
	}
	detail := fmt.Sprintf("最近 %d 分钟 %d 次调用，失败 %d 次（%.1f%%），P95 %s，阈值 %s。", statusWindowMinutes, result.calls, result.errors, math.Round(rate*10)/10, p95, formatLatency(svc.SlowMS))
	switch {
	case result.state == stateDown:
		return svc.Name + "不可用", detail
	case result.errors >= minErrorsForRate && rate/100 > degradedErrorRate:
		return svc.Name + "错误率升高", detail
	}
	return svc.Name + "响应变慢", detail
}

// formatLatency writes milliseconds the way the console does: 310ms, 1.8s.
func formatLatency(ms int) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%dms", ms)
}
