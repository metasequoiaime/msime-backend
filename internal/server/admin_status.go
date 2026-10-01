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
	// statusProbeInterval is how often statusProbeJob runs; its runs are aligned to the start of each minute so that every replica's runs line up.
	statusProbeInterval = time.Minute
	// statusFlushDelay is how long after the start of a minute every replica pings the database and flushes its metric buckets.
	statusFlushDelay = 2 * time.Second
	// statusJudgeDelay is how long after the start of a minute the leader judges, leaving the other replicas time to flush the minute that just ended.
	statusJudgeDelay = 20 * time.Second
	// statusWindowMinutes is the window of recent calls a probe judges a service by: the last five complete minutes.
	statusWindowMinutes = 5
	// statusStreakLookback is how recent a service's previous verdict must be to continue its streak; after a longer gap without a leader the streaks start afresh.
	statusStreakLookback = 10 * time.Minute
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

// probeResult is one service's state in a probe and the window it was judged on.
type probeResult struct {
	state  string
	calls  int64
	errors int64
	p95    *int
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
	checked, states, err := s.statusStates(ctx)
	if err != nil {
		slog.Error("admin status failed", "reason", err.Error())
		fail(w, 503, "auth_unavailable")
		return
	}
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
	services := s.statusServices()
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
	respond(w, 200, map[string]any{"checked_at": checkedAt, "state": statusHealth(checked, states), "services": out, "incidents": list})
}

// statusStates reads the latest probe: the verdicts of the newest minute the status probe's leader judged, which every replica reports alike. checked is when that probe ran, zero before the first one and always without a database, where no probe runs.
func (s *Server) statusStates(ctx context.Context) (time.Time, map[string]probeResult, error) {
	if s.accounts == nil {
		return time.Time{}, map[string]probeResult{}, nil
	}
	verdicts, err := s.accounts.LatestServiceVerdicts(ctx)
	if err != nil {
		return time.Time{}, nil, err
	}
	var checked time.Time
	states := make(map[string]probeResult, len(verdicts))
	for _, v := range verdicts {
		states[v.Service] = probeResult{state: v.State, calls: v.Calls, errors: v.Errors, p95: v.P95MS}
		if v.CheckedAt.After(checked) {
			checked = v.CheckedAt
		}
	}
	return checked, states, nil
}

// adminHealth is the overall state for the shell, from the latest probe. A replica that cannot read the probe reports down, since the database it is stored in is unreachable from it, as the probe itself would have found.
func (s *Server) adminHealth(ctx context.Context) string {
	checked, states, err := s.statusStates(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return stateUnknown
		}
		slog.Warn("admin health: status read failed", "reason", err.Error())
		return stateDown
	}
	return statusHealth(checked, states)
}

// statusHealth is ok, degraded or down, or unknown while no probe has run or the last one is stale (the leader is gone and no replica has taken over yet). The database being down is down; any upstream service degraded or down is degraded.
func statusHealth(checked time.Time, states map[string]probeResult) string {
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

// statusProbeJob runs the status probe once at startup and then every minute until ctx ends. Every replica pings the database and flushes its metric buckets each minute (statusCollect); only the replica holding the status leader lock judges the services, rolls the minute into admin_service_daily and opens or resolves automatic incidents (statusJudge). The final flush on shutdown is left to Close, which runs it only after the streaming sessions it cancels have recorded their calls; leadership is given up when the job ends, so another replica takes over at its next minute.
func (s *Server) statusProbeJob(ctx context.Context) {
	if s.accounts == nil {
		return
	}
	defer s.releaseStatusLeader()
	s.statusTick(ctx, time.Now())
	for {
		minute := time.Now().Truncate(statusProbeInterval).Add(statusProbeInterval)
		if !sleepUntil(ctx, minute.Add(statusFlushDelay)) {
			return
		}
		pingErr := s.statusCollect(ctx)
		if !sleepUntil(ctx, minute.Add(statusJudgeDelay)) {
			return
		}
		s.statusJudge(ctx, time.Now(), pingErr)
	}
}

// sleepUntil waits until at and reports false if ctx ended first.
func sleepUntil(ctx context.Context, at time.Time) bool {
	timer := time.NewTimer(time.Until(at))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// statusTick is one whole probe at now: this replica's collection followed, if it leads, by the judgement.
func (s *Server) statusTick(ctx context.Context, now time.Time) {
	pingErr := s.statusCollect(ctx)
	if ctx.Err() == nil {
		s.statusJudge(ctx, now, pingErr)
	}
}

// statusCollect is every replica's share of a probe: ping the database, record the ping as a call of the database service, and flush this process's metric buckets so the leader judges its calls together with the other replicas'. It returns the ping's error.
func (s *Server) statusCollect(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	started := time.Now()
	pingErr := s.accounts.PingDatabase(pingCtx)
	pingCancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.observeCall(databaseService, time.Now(), time.Since(started), pingErr != nil, 0)
	if err := s.flushMetrics(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("status probe: metrics flush failed", "reason", err.Error())
	}
	return pingErr
}

// statusJudge is the leader's share of a probe at now: judge every service by the calls all replicas recorded in the last five complete minutes, continue the streaks from the previous stored verdicts, open or resolve automatic incidents, and store the verdicts, which also rolls the minute into admin_service_daily once. A replica that does not lead returns at once.
func (s *Server) statusJudge(ctx context.Context, now time.Time, pingErr error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	logFailure := func(what string, err error) {
		if err != nil && ctx.Err() == nil {
			slog.Warn("status probe: "+what+" failed", "reason", err.Error())
		}
	}
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	if !s.leadStatus(ctx, logFailure) {
		return
	}
	now = now.UTC()
	minute := now.Truncate(time.Minute)
	recent, err := s.accounts.ServiceMinutes(ctx, minute.Add(-statusWindowMinutes*time.Minute), minute)
	if err != nil {
		logFailure("window read", err)
		return
	}
	windows := map[string]*metricCounts{}
	for _, row := range recent {
		if windows[row.Service] == nil {
			windows[row.Service] = &metricCounts{}
		}
		windows[row.Service].add(metricCounts{calls: row.Calls, errors: row.Errors, latency: histogramFromJSON(row.Latency)})
	}
	previous, err := s.accounts.PreviousServiceVerdicts(ctx, minute.Add(-statusStreakLookback), minute)
	if err != nil {
		logFailure("previous verdicts read", err)
		return
	}
	open, err := s.accounts.OpenAutoIncidents(ctx)
	if err != nil {
		logFailure("incidents read", err)
		return
	}
	dayStart := now.Truncate(24 * time.Hour)
	// Today's P95 per service from the stored hours plus whatever this process has not flushed yet.
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
	services := s.statusServices()
	verdicts := make([]account.ServiceVerdict, 0, len(services))
	for _, svc := range services {
		var window metricCounts
		if w := windows[svc.Key]; w != nil {
			window = *w
		}
		state := judgeService(window, svc.SlowMS)
		if svc.Key == databaseService && pingErr != nil {
			state = stateDown
		}
		result := probeResult{state: state, calls: window.calls, errors: window.errors}
		if p95, ok := window.latency.percentile(0.95); ok {
			result.p95 = &p95
		}
		v := account.ServiceVerdict{Service: svc.Key, State: state, Calls: result.calls, Errors: result.errors, P95MS: result.p95, CheckedAt: time.Now().UTC()}
		v.Daily = account.ServiceProbe{Available: state != stateDown, Degraded: state == stateDegraded || state == stateDown}
		if c := today[svc.Key]; c != nil {
			if p95, ok := c.latency.percentile(0.95); ok {
				v.Daily.P95MS = &p95
			}
		}
		prev, hasPrev := previous[svc.Key]
		s.advanceStreak(ctx, svc, result, prev, hasPrev, open[svc.Key], &v, logFailure)
		verdicts = append(verdicts, v)
	}
	_, err = s.accounts.RecordServiceVerdicts(ctx, minute, verdicts)
	logFailure("verdicts", err)
	if now.Sub(s.statusPruned) >= time.Hour {
		if err := s.accounts.PruneServiceMonitoring(ctx); err != nil {
			logFailure("retention", err)
		} else {
			s.statusPruned = now
		}
	}
}

// advanceStreak continues a service's good or bad streak from its previous verdict into v, opening an automatic incident after incidentOpenAfter bad probes and resolving the open one after incidentResolveAfter good ones. The streaks live in the stored verdicts rather than in this process, so a new leader carries on where the last one stopped; open is read from admin_incidents, so a leader never acts on a stale belief about an incident another replica opened or resolved.
func (s *Server) advanceStreak(ctx context.Context, svc monitoredService, result probeResult, previous account.ServiceVerdict, hasPrevious, open bool, v *account.ServiceVerdict, logFailure func(string, error)) {
	if result.state == stateDegraded || result.state == stateDown {
		v.BadRuns = 1
		if hasPrevious && previous.BadRuns > 0 {
			v.BadRuns, v.Incident = previous.BadRuns+1, previous.Incident
		}
		// An open automatic incident belongs to this streak whoever opened it; once one has been opened in the streak, an admin resolving it while the service still fails is not overridden.
		v.Incident = v.Incident || open
		if v.BadRuns >= incidentOpenAfter && !v.Incident {
			title, description := incidentText(svc, result)
			_, err := s.accounts.OpenAutoIncident(ctx, svc.Key, title, description)
			logFailure("incident open", err)
			v.Incident = err == nil
		}
		return
	}
	v.GoodRuns = 1
	if hasPrevious && previous.GoodRuns > 0 {
		v.GoodRuns = previous.GoodRuns + 1
	}
	if v.GoodRuns >= incidentResolveAfter && open {
		_, err := s.accounts.ResolveAutoIncident(ctx, svc.Key)
		logFailure("incident resolve", err)
	}
}

// leadStatus reports whether this replica leads the status probe. A held lock is checked first, since its session may have died with the connection; otherwise the lock is tried again, so a replica takes over within a probe of the leader's session ending. The caller holds statusMu.
func (s *Server) leadStatus(ctx context.Context, logFailure func(string, error)) bool {
	if s.statusLeader != nil {
		err := s.statusLeader.Check(ctx)
		if err == nil {
			return true
		}
		logFailure("leadership check", err)
		s.statusLeader.Release()
		s.statusLeader = nil
		slog.Warn("status probe: leadership lost")
	}
	leader, err := s.accounts.AcquireStatusLeader(ctx)
	if err != nil {
		logFailure("leadership", err)
		return false
	}
	if leader == nil {
		return false
	}
	s.statusLeader = leader
	slog.Info("status probe: leading")
	return true
}

// releaseStatusLeader gives up the status probe's leadership, if held.
func (s *Server) releaseStatusLeader() {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.statusLeader.Release()
	s.statusLeader = nil
}

// statusServices is the status page's own database probe followed by the monitored upstream services.
func (s *Server) statusServices() []monitoredService {
	return append([]monitoredService{{Key: databaseService, Name: "数据库", Provider: "PostgreSQL", SlowMS: databaseSlowMS}}, s.monitoredServices()...)
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
