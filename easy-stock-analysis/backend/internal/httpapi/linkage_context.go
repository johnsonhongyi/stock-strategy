package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"easy-stock/backend/internal/foundation"
	"easy-stock/backend/internal/linkage"
)

type linkageContextSnapshot struct {
	SchemaVersion int                      `json:"schema_version"`
	Source        string                   `json:"source"`
	SnapshotID    string                   `json:"snapshot_id"`
	Version       int64                    `json:"version"`
	AsOf          string                   `json:"as_of,omitempty"`
	ReceivedAt    string                   `json:"received_at"`
	TradeDate     string                   `json:"trade_date,omitempty"`
	SessionPhase  string                   `json:"session_phase"`
	Freshness     string                   `json:"freshness"`
	ExpiresAt     string                   `json:"expires_at"`
	Errors        []string                 `json:"errors,omitempty"`
	Sources       []linkageContextSource   `json:"sources"`
	Coverage      []linkageCoverage        `json:"coverage"`
	Themes        []linkageTheme           `json:"themes"`
	Heat          linkageHeatSnapshot      `json:"heat"`
	Ladder        *linkageLadderSnapshot   `json:"ladder,omitempty"`
	Evidence      []linkageContextEvidence `json:"evidence,omitempty"`
}

type linkageContextEvidence struct {
	SourceID   string          `json:"source_id"`
	SourceName string          `json:"source_name"`
	NodeID     string          `json:"node_id"`
	SnapshotID string          `json:"snapshot_id"`
	TradeDate  string          `json:"trade_date"`
	AsOf       string          `json:"as_of"`
	ReceivedAt string          `json:"received_at"`
	Freshness  string          `json:"freshness"`
	Payload    json.RawMessage `json:"payload"`
}

type linkageContextSource struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Source    string `json:"source,omitempty"`
	TradeDate string `json:"trade_date,omitempty"`
	AsOf      string `json:"as_of,omitempty"`
	Freshness string `json:"freshness"`
	Error     string `json:"error,omitempty"`
}

type linkageCoverage struct {
	Scope         string   `json:"scope"`
	TotalExpected *int     `json:"total_expected,omitempty"`
	ObservedCount int      `json:"observed_count"`
	Coverage      *float64 `json:"coverage,omitempty"`
	Complete      bool     `json:"complete"`
	IsSample      bool     `json:"is_sample"`
	Source        string   `json:"source"`
	TradeDate     string   `json:"trade_date,omitempty"`
}

type linkageTheme struct {
	ThemeID              string              `json:"theme_id"`
	Name                 string              `json:"name"`
	Aliases              []string            `json:"aliases,omitempty"`
	Source               string              `json:"source"`
	TradeDate            string              `json:"trade_date,omitempty"`
	SnapshotID           string              `json:"snapshot_id,omitempty"`
	TrendStage           string              `json:"trend_stage,omitempty"`
	ChangePercent        float64             `json:"change_percent,omitempty"`
	TrendScore           int                 `json:"trend_score,omitempty"`
	DailyStrengthScore   int                 `json:"daily_strength_score,omitempty"`
	FiveDayStrengthScore int                 `json:"five_day_strength_score,omitempty"`
	ActiveDays           int                 `json:"active_days,omitempty"`
	Coverage             linkageCoverage     `json:"coverage"`
	Members              []linkageMembership `json:"members"`
}

type linkageMembership struct {
	Symbol           string   `json:"symbol"`
	ThemeID          string   `json:"theme_id"`
	MembershipKind   string   `json:"membership_kind"`
	MembershipSource string   `json:"membership_source"`
	EvidenceTime     string   `json:"evidence_time,omitempty"`
	Confidence       *float64 `json:"confidence,omitempty"`
	Role             string   `json:"role,omitempty"`
}

type linkageHeatSnapshot struct {
	Scope     string                 `json:"scope"`
	UpdatedAt string                 `json:"updated_at,omitempty"`
	Stale     bool                   `json:"stale"`
	Sources   []hotStockSourceStatus `json:"sources"`
	Stocks    []linkageHeatStock     `json:"stocks"`
}

type linkageHeatStock struct {
	Symbol      string         `json:"symbol"`
	Name        string         `json:"name"`
	SourceCount int            `json:"source_count"`
	Ranks       map[string]int `json:"ranks"`
}

type linkageLadderSnapshot struct {
	SessionStatus   string                `json:"session_status"`
	ComparisonReady bool                  `json:"comparison_ready"`
	Current         linkageLadderDay      `json:"current"`
	Previous        linkageLadderDay      `json:"previous"`
	ConceptStatus   string                `json:"concept_status"`
	Meta            foundation.SourceMeta `json:"meta"`
}

type linkageLadderDay struct {
	TradeDate       string               `json:"trade_date"`
	LimitUpCount    int                  `json:"limit_up_count"`
	BoardCount      int                  `json:"board_count"`
	FirstBoardCount int                  `json:"first_board_count"`
	MaxStreak       int                  `json:"max_streak"`
	ReopenedCount   int                  `json:"reopened_count"`
	STCount         int                  `json:"st_count"`
	Levels          []linkageLadderLevel `json:"levels"`
}

type linkageLadderLevel struct {
	Level  int                  `json:"level"`
	Label  string               `json:"label"`
	Count  int                  `json:"count"`
	Stocks []linkageLadderStock `json:"stocks"`
}

type linkageLadderStock struct {
	Symbol          string   `json:"symbol"`
	Name            string   `json:"name"`
	Streak          int      `json:"streak"`
	FirstLimitTime  string   `json:"first_limit_time,omitempty"`
	LastLimitTime   string   `json:"last_limit_time,omitempty"`
	OpenCount       int      `json:"open_count"`
	PrimaryTheme    string   `json:"primary_theme,omitempty"`
	SecondaryThemes []string `json:"secondary_themes,omitempty"`
	ThemeSource     string   `json:"theme_source,omitempty"`
	ThemeLeaderRole string   `json:"theme_leader_role,omitempty"`
	ThemeConfidence *float64 `json:"theme_confidence,omitempty"`
}

type linkageContextResult struct {
	kind  string
	value any
	err   error
}

func (s *Server) linkageContextHandler(w http.ResponseWriter, r *http.Request) {
	if s.linkage == nil {
		writeError(w, http.StatusServiceUnavailable, "desktop linkage is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	results := make(chan linkageContextResult, 3)
	go func() {
		if s.themeOverview == nil {
			results <- linkageContextResult{kind: "themes", err: errors.New("theme overview provider is unavailable")}
			return
		}
		items, meta, err := s.themeOverview.Overviews(ctx)
		results <- linkageContextResult{kind: "themes", value: struct {
			Items []foundation.ThemeOverview
			Meta  foundation.SourceMeta
		}{items, meta}, err: err}
	}()
	go func() {
		data, err := s.loadHotStockRanks(ctx, false)
		results <- linkageContextResult{kind: "heat", value: data, err: err}
	}()
	go func() {
		if s.limitUpProvider == nil {
			results <- linkageContextResult{kind: "ladder", err: errors.New("limit-up provider is unavailable")}
			return
		}
		data, err := s.limitUpSnapshots.load(ctx, s.limitUpProvider, s.stockConcepts, s.realtimeProvider)
		results <- linkageContextResult{kind: "ladder", value: data, err: err}
	}()

	var themes []foundation.ThemeOverview
	var themeMeta foundation.SourceMeta
	var heat hotStockRankData
	var ladder limitUpLadderData
	var themeErr, heatErr, ladderErr error
	for received := 0; received < 3; received++ {
		select {
		case result := <-results:
			switch result.kind {
			case "themes":
				themeErr = result.err
				if result.err == nil {
					value := result.value.(struct {
						Items []foundation.ThemeOverview
						Meta  foundation.SourceMeta
					})
					themes, themeMeta = value.Items, value.Meta
				}
			case "heat":
				heatErr = result.err
				if result.err == nil {
					heat = result.value.(hotStockRankData)
				}
			case "ladder":
				ladderErr = result.err
				if result.err == nil {
					ladder = result.value.(limitUpLadderData)
				}
			}
		case <-ctx.Done():
			received = 3
		}
	}

	now := time.Now().UTC()
	snapshot := buildLinkageContextSnapshot(now, themes, themeMeta, themeErr, heat, heatErr, ladder, ladderErr)
	evidence, evidenceErr := s.linkage.RecentContextEvidence(r.Context())
	if evidenceErr != nil {
		snapshot.Errors = append(snapshot.Errors, "context_evidence: unavailable")
		if snapshot.Freshness == "fresh" {
			snapshot.Freshness = "partial"
		}
	} else {
		snapshot = mergeLinkageContextEvidence(snapshot, evidence)
	}
	s.linkageContextMu.Lock()
	defer s.linkageContextMu.Unlock()
	previous, previousErr := s.linkage.LatestContextSnapshot(r.Context())
	if previousErr != nil && !errors.Is(previousErr, sql.ErrNoRows) {
		writeError(w, http.StatusServiceUnavailable, "could not load previous linkage context")
		return
	}
	events := deriveLinkageContextEvents(previous, snapshot)
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not encode linkage context")
		return
	}
	if err := s.linkage.StoreContextSnapshot(r.Context(), snapshot.SnapshotID, snapshot.TradeDate, encoded, events); err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not persist linkage context")
		return
	}
	w.Header().Set("ETag", `"`+snapshot.SnapshotID+`"`)
	w.Header().Set("Cache-Control", "private, no-cache")
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) linkageEventsHandler(w http.ResponseWriter, r *http.Request) {
	if s.linkage == nil {
		writeError(w, http.StatusServiceUnavailable, "desktop linkage is not configured")
		return
	}
	limit := 100
	if value := r.URL.Query().Get("limit"); value != "" {
		if _, err := fmt.Sscan(value, &limit); err != nil || limit < 1 || limit > 500 {
			writeError(w, http.StatusBadRequest, "invalid event limit")
			return
		}
	}
	var events []linkage.ContextEvent
	var err error
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("latest")), "true") {
		events, err = s.linkage.LatestContextEvents(r.Context(), limit)
	} else {
		events, err = s.linkage.ContextEvents(r.Context(), strings.TrimSpace(r.URL.Query().Get("after")), limit)
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not load linkage events")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": 1, "events": events})
}

func buildLinkageContextSnapshot(now time.Time, overviews []foundation.ThemeOverview, themeMeta foundation.SourceMeta, themeErr error, heat hotStockRankData, heatErr error, ladder limitUpLadderData, ladderErr error) linkageContextSnapshot {
	snapshotID := fmt.Sprintf("stock-strategy-context-%d", now.UnixNano())
	tradeDate := ladder.Current.TradeDate
	snapshot := linkageContextSnapshot{
		SchemaVersion: 1, Source: "stock-strategy.go", SnapshotID: snapshotID, Version: now.UnixNano(),
		ReceivedAt: now.Format(time.RFC3339Nano), TradeDate: tradeDate, SessionPhase: ladder.SessionStatus,
		ExpiresAt: now.Add(30 * time.Second).Format(time.RFC3339Nano),
		Sources:   []linkageContextSource{}, Coverage: []linkageCoverage{}, Themes: []linkageTheme{},
		Heat: linkageHeatSnapshot{Scope: "top_100_per_source", Stocks: []linkageHeatStock{}, Sources: []hotStockSourceStatus{}},
	}
	if snapshot.SessionPhase == "" {
		snapshot.SessionPhase = "unknown"
	}
	addSource := func(source linkageContextSource) { snapshot.Sources = append(snapshot.Sources, source) }
	if themeErr != nil {
		snapshot.Errors = append(snapshot.Errors, "themes: "+themeErr.Error())
		addSource(linkageContextSource{ID: "stock-strategy.themes", Name: "题材概览", Status: "unavailable", Freshness: "unknown", Error: themeErr.Error()})
	} else if len(overviews) == 0 {
		snapshot.Errors = append(snapshot.Errors, "themes: no_theme_snapshot")
		addSource(linkageContextSource{ID: "stock-strategy.themes", Name: "题材概览", Status: "unavailable", Freshness: "unknown", Error: "no_theme_snapshot"})
	} else {
		freshness := linkageFreshness(themeMeta.FetchedAt, 2*time.Minute, themeMeta.Stale || themeMeta.CarryForward)
		addSource(linkageContextSource{ID: "stock-strategy.themes", Name: "题材概览", Status: "available", Source: themeMeta.Source, TradeDate: themeMeta.TradeDate, AsOf: timeString(themeMeta.FetchedAt), Freshness: freshness})
		snapshot.AsOf = minTimeString(snapshot.AsOf, timeString(themeMeta.FetchedAt))
	}
	if heatErr != nil {
		snapshot.Errors = append(snapshot.Errors, "heat: "+heatErr.Error())
		addSource(linkageContextSource{ID: "stock-strategy.heat", Name: "人气排名", Status: "unavailable", Freshness: "unknown", Error: heatErr.Error()})
	} else if len(heat.Sources) == 0 {
		snapshot.Errors = append(snapshot.Errors, "heat: no_source_snapshots")
		addSource(linkageContextSource{ID: "stock-strategy.heat", Name: "人气排名", Status: "unavailable", Freshness: "unknown", Error: "no_source_snapshots"})
	} else {
		heatStocks := make([]linkageHeatStock, 0, len(heat.Stocks))
		for _, stock := range heat.Stocks {
			heatStocks = append(heatStocks, linkageHeatStock{Symbol: stock.Symbol, Name: stock.Name, SourceCount: stock.SourceCount, Ranks: stock.Ranks})
		}
		snapshot.Heat = linkageHeatSnapshot{Scope: "top_100_per_source", UpdatedAt: timeString(heat.UpdatedAt), Stale: heat.Stale, Sources: heat.Sources, Stocks: heatStocks}
		freshness := linkageFreshness(heat.UpdatedAt, 30*time.Minute, heat.Stale)
		addSource(linkageContextSource{ID: "stock-strategy.heat", Name: "人气排名", Status: "available", TradeDate: tradeDate, AsOf: timeString(heat.UpdatedAt), Freshness: freshness})
		snapshot.AsOf = minTimeString(snapshot.AsOf, timeString(heat.UpdatedAt))
		for _, source := range heat.Sources {
			coverage := float64(source.Count) / hotStockRankLimit
			if coverage > 1 {
				coverage = 1
			}
			count, ratio := hotStockRankLimit, coverage
			snapshot.Coverage = append(snapshot.Coverage, linkageCoverage{Scope: "top_100_observation_range", TotalExpected: &count, ObservedCount: source.Count, Coverage: &ratio, Complete: false, IsSample: true, Source: source.ID, TradeDate: tradeDate})
		}
	}
	if ladderErr != nil {
		snapshot.Errors = append(snapshot.Errors, "ladder: "+ladderErr.Error())
		addSource(linkageContextSource{ID: "stock-strategy.limit-up-ladder", Name: "连板天梯", Status: "unavailable", Freshness: "unknown", Error: ladderErr.Error()})
	} else if ladder.Current.TradeDate == "" {
		snapshot.Errors = append(snapshot.Errors, "ladder: no_current_trade_date")
		addSource(linkageContextSource{ID: "stock-strategy.limit-up-ladder", Name: "连板天梯", Status: "unavailable", Freshness: "unknown", Error: "no_current_trade_date"})
	} else {
		snapshot.Ladder = normalizeLinkageLadder(ladder)
		freshness := linkageFreshness(ladder.Meta.FetchedAt, 30*time.Second, ladder.Meta.Stale || ladder.Meta.CarryForward)
		addSource(linkageContextSource{ID: "stock-strategy.limit-up-ladder", Name: "连板天梯", Status: "available", Source: ladder.Meta.Source, TradeDate: ladder.Current.TradeDate, AsOf: timeString(ladder.Meta.FetchedAt), Freshness: freshness})
		snapshot.AsOf = minTimeString(snapshot.AsOf, timeString(ladder.Meta.FetchedAt))
		count := ladder.Current.LimitUpCount
		snapshot.Coverage = append(snapshot.Coverage, linkageCoverage{Scope: "provider_current_limit_up_pool", TotalExpected: nil, ObservedCount: count, Complete: false, IsSample: true, Source: ladder.Meta.Source, TradeDate: ladder.Current.TradeDate})
	}
	if len(overviews) > 0 {
		snapshot.Themes = normalizeLinkageThemes(overviews, tradeDate)
		snapshot.Coverage = append(snapshot.Coverage, linkageCoverage{Scope: "provider_returned_theme_overviews", ObservedCount: len(overviews), Complete: false, IsSample: true, Source: themeMeta.Source, TradeDate: themeMeta.TradeDate})
	}
	if ladderErr == nil {
		snapshot.Themes = mergeLadderThemeMemberships(snapshot.Themes, ladder)
	}
	addSource(linkageContextSource{ID: "tk.auction", Name: "TK 竞价", Status: "unavailable", Freshness: "unknown", Error: "source_not_connected_to_go"})
	if len(snapshot.Errors) == 0 && themeErr == nil && heatErr == nil && ladderErr == nil {
		snapshot.Freshness = "fresh"
	} else if snapshot.AsOf == "" {
		snapshot.Freshness = "unavailable"
	} else {
		snapshot.Freshness = "partial"
	}
	for _, source := range snapshot.Sources {
		if source.Status != "available" && snapshot.Freshness == "fresh" {
			snapshot.Freshness = "partial"
		}
		if source.Freshness == "unknown" && snapshot.Freshness == "fresh" {
			snapshot.Freshness = "partial"
		}
		if source.TradeDate != "" && snapshot.TradeDate != "" && source.TradeDate != snapshot.TradeDate {
			snapshot.Errors = append(snapshot.Errors, "trade_date_mismatch:"+source.ID)
			snapshot.Freshness = "partial"
		}
		if source.Freshness == "stale" && snapshot.Freshness == "fresh" {
			snapshot.Freshness = "stale"
		}
	}
	return snapshot
}

func mergeLinkageContextEvidence(snapshot linkageContextSnapshot, stored []linkage.ContextEvidence) linkageContextSnapshot {
	latest := map[string]linkage.ContextEvidence{}
	var latestAnomaly linkage.ContextEvidence
	anomalyEvents := make([]map[string]any, 0)
	seenAnomalyEvents := map[string]bool{}
	for _, item := range stored {
		current, exists := latest[item.SourceID]
		if !exists || item.TradeDate > current.TradeDate ||
			(item.TradeDate == current.TradeDate && item.AsOf > current.AsOf) {
			latest[item.SourceID] = item
		}
		if item.SourceID == "tk.anomaly" {
			if latestAnomaly.SnapshotID == "" || item.TradeDate > latestAnomaly.TradeDate ||
				(item.TradeDate == latestAnomaly.TradeDate && item.AsOf > latestAnomaly.AsOf) {
				latestAnomaly = item
			}
			var payload struct {
				Events []map[string]any `json:"events"`
			}
			if json.Unmarshal(item.Payload, &payload) == nil {
				for _, event := range payload.Events {
					if eventID, ok := event["event_id"].(string); ok && eventID != "" && !seenAnomalyEvents[eventID] {
						seenAnomalyEvents[eventID] = true
						anomalyEvents = append(anomalyEvents, event)
					}
				}
			}
		}
	}
	if latestAnomaly.SnapshotID != "" {
		sort.Slice(anomalyEvents, func(i, j int) bool {
			left, _ := anomalyEvents[i]["observed_at"].(string)
			right, _ := anomalyEvents[j]["observed_at"].(string)
			if left == right {
				return fmt.Sprint(anomalyEvents[i]["event_id"]) < fmt.Sprint(anomalyEvents[j]["event_id"])
			}
			return left < right
		})
		if len(anomalyEvents) > 100 {
			anomalyEvents = anomalyEvents[len(anomalyEvents)-100:]
		}
		if payload, err := json.Marshal(map[string]any{"schema_version": 1, "source_id": "tk.anomaly", "events": anomalyEvents}); err == nil {
			latestAnomaly.Payload = payload
			latest["tk.anomaly"] = latestAnomaly
		}
	}
	sourceIndexes := map[string]int{}
	for index, source := range snapshot.Sources {
		sourceIndexes[source.ID] = index
	}
	snapshot.Evidence = make([]linkageContextEvidence, 0, len(latest))
	for sourceID, item := range latest {
		name := linkageEvidenceSources[sourceID]
		if name == "" {
			name = sourceID
		}
		freshness := "historical"
		if snapshot.TradeDate != "" {
			if item.TradeDate == snapshot.TradeDate {
				freshness = "fresh"
			} else {
				freshness = "stale"
			}
		}
		var sourceState struct {
			Freshness string `json:"source_freshness"`
		}
		if json.Unmarshal(item.Payload, &sourceState) == nil && sourceState.Freshness == "stale" {
			freshness = "stale"
		}
		evidence := linkageContextEvidence{SourceID: sourceID, SourceName: name, NodeID: item.NodeID,
			SnapshotID: item.SnapshotID, TradeDate: item.TradeDate, AsOf: item.AsOf,
			ReceivedAt: item.ReceivedAt, Freshness: freshness, Payload: append(json.RawMessage(nil), item.Payload...)}
		snapshot.Evidence = append(snapshot.Evidence, evidence)
		source := linkageContextSource{ID: sourceID, Name: name, Status: "available", Source: item.NodeID,
			TradeDate: item.TradeDate, AsOf: item.AsOf, Freshness: freshness}
		if index, exists := sourceIndexes[sourceID]; exists {
			snapshot.Sources[index] = source
		} else {
			sourceIndexes[sourceID] = len(snapshot.Sources)
			snapshot.Sources = append(snapshot.Sources, source)
		}
		snapshot.AsOf = minTimeString(snapshot.AsOf, item.AsOf)
	}
	sort.Slice(snapshot.Sources, func(i, j int) bool { return snapshot.Sources[i].ID < snapshot.Sources[j].ID })
	sort.Slice(snapshot.Evidence, func(i, j int) bool { return snapshot.Evidence[i].SourceID < snapshot.Evidence[j].SourceID })
	if snapshot.AsOf == "" {
		snapshot.Freshness = "unavailable"
		return snapshot
	}
	snapshot.Freshness = "fresh"
	for _, source := range snapshot.Sources {
		if source.Status != "available" || source.Freshness == "unknown" {
			snapshot.Freshness = "partial"
		}
		if source.Freshness == "stale" && snapshot.Freshness == "fresh" {
			snapshot.Freshness = "stale"
		}
	}
	if len(snapshot.Errors) > 0 && snapshot.Freshness == "fresh" {
		snapshot.Freshness = "partial"
	}
	return snapshot
}

func linkageFreshness(fetchedAt time.Time, maxAge time.Duration, sourceStale bool) string {
	if fetchedAt.IsZero() {
		return "unknown"
	}
	if sourceStale || time.Since(fetchedAt) > maxAge {
		return "stale"
	}
	return "fresh"
}

func normalizeLinkageLadder(data limitUpLadderData) *linkageLadderSnapshot {
	return &linkageLadderSnapshot{
		SessionStatus: data.SessionStatus, ComparisonReady: data.ComparisonReady,
		Current: normalizeLinkageLadderDay(data.Current), Previous: normalizeLinkageLadderDay(data.Previous),
		ConceptStatus: data.ConceptStatus, Meta: data.Meta,
	}
}

func normalizeLinkageLadderDay(day limitUpLadderDay) linkageLadderDay {
	result := linkageLadderDay{TradeDate: day.TradeDate, LimitUpCount: day.LimitUpCount, BoardCount: day.BoardCount,
		FirstBoardCount: day.FirstBoardCount, MaxStreak: day.MaxStreak, ReopenedCount: day.ReopenedCount,
		STCount: day.STCount, Levels: []linkageLadderLevel{}}
	for _, level := range day.Levels {
		entry := linkageLadderLevel{Level: level.Level, Label: level.Label, Count: level.Count, Stocks: []linkageLadderStock{}}
		for _, stock := range level.Stocks {
			entry.Stocks = append(entry.Stocks, linkageLadderStock{
				Symbol: canonicalLinkageSymbol(stock.Symbol), Name: stock.Name, Streak: stock.Streak,
				FirstLimitTime: stock.FirstLimitTime, LastLimitTime: stock.LastLimitTime, OpenCount: stock.OpenCount,
				PrimaryTheme: stock.PrimaryTheme, SecondaryThemes: append([]string(nil), stock.SecondaryThemes...),
				ThemeSource: stock.ThemeSource, ThemeLeaderRole: stock.ThemeLeaderRole,
				ThemeConfidence: optionalPositive(stock.ThemeConfidence),
			})
		}
		result.Levels = append(result.Levels, entry)
	}
	return result
}

func optionalPositive(value float64) *float64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func normalizeLinkageThemes(overviews []foundation.ThemeOverview, tradeDate string) []linkageTheme {
	themes := make([]linkageTheme, 0, len(overviews))
	for _, overview := range overviews {
		name := strings.TrimSpace(overview.Name)
		key := strings.TrimSpace(overview.Theme)
		if key == "" {
			key = name
		}
		if key == "" {
			continue
		}
		source := strings.TrimSpace(overview.Source)
		if source == "" {
			source = "unknown"
		}
		themeID := "provider:" + source + ":" + key
		members := make([]linkageMembership, 0, len(overview.LeaderStocks))
		for _, stock := range overview.LeaderStocks {
			symbol := canonicalLinkageSymbol(stock.Symbol)
			if symbol == "" {
				continue
			}
			members = append(members, linkageMembership{Symbol: symbol, ThemeID: themeID, MembershipKind: "leader_sample", MembershipSource: source, EvidenceTime: overview.TradeDate})
		}
		expected := overview.TotalNodes
		var total *int
		var ratio *float64
		if expected > 0 {
			count := expected
			total = &count
			value := float64(len(members)) / float64(expected)
			if value > 1 {
				value = 1
			}
			ratio = &value
		}
		coverage := linkageCoverage{Scope: "leader_stock_sample", TotalExpected: total, ObservedCount: len(members), Coverage: ratio, Complete: expected > 0 && len(members) >= expected, IsSample: len(members) < expected || expected <= 0, Source: source, TradeDate: overview.TradeDate}
		date := overview.TradeDate
		if date == "" {
			date = tradeDate
		}
		themes = append(themes, linkageTheme{ThemeID: themeID, Name: name, Aliases: append([]string(nil), overview.Aliases...), Source: source, TradeDate: date, SnapshotID: overview.SnapshotID, TrendStage: overview.TrendStage, ChangePercent: overview.ChangePercent, TrendScore: overview.TrendScore, DailyStrengthScore: overview.DailyStrengthScore, FiveDayStrengthScore: overview.FiveDayStrengthScore, ActiveDays: overview.ActiveDays, Coverage: coverage, Members: members})
	}
	return themes
}

func mergeLadderThemeMemberships(themes []linkageTheme, ladder limitUpLadderData) []linkageTheme {
	nameToIDs := map[string][]string{}
	for _, theme := range themes {
		for _, name := range append([]string{theme.Name}, theme.Aliases...) {
			key := strings.ToLower(strings.TrimSpace(name))
			if key == "" {
				continue
			}
			found := false
			for _, existing := range nameToIDs[key] {
				if existing == theme.ThemeID {
					found = true
					break
				}
			}
			if !found {
				nameToIDs[key] = append(nameToIDs[key], theme.ThemeID)
			}
		}
	}
	byID := make(map[string]int, len(themes))
	for index := range themes {
		byID[themes[index].ThemeID] = index
	}
	for _, level := range ladder.Current.Levels {
		for _, stock := range level.Stocks {
			symbol := canonicalLinkageSymbol(stock.Symbol)
			if symbol == "" {
				continue
			}
			for _, candidate := range append([]string{stock.PrimaryTheme}, stock.SecondaryThemes...) {
				name := strings.TrimSpace(candidate)
				if name == "" {
					continue
				}
				themeIDs := nameToIDs[strings.ToLower(name)]
				if len(themeIDs) == 0 {
					themeIDs = []string{"unmapped:" + shortHash(strings.ToLower(name))}
				}
				kind := "ladder_primary"
				if name != stock.PrimaryTheme {
					kind = "ladder_secondary"
				}
				source := strings.TrimSpace(stock.ThemeSource)
				if source == "" {
					source = "stock-strategy.limit-up-ladder"
				}
				var confidence *float64
				if stock.ThemeConfidence > 0 {
					value := stock.ThemeConfidence
					confidence = &value
				}
				for _, themeID := range themeIDs {
					index, exists := byID[themeID]
					if !exists {
						index = len(themes)
						byID[themeID] = index
						themes = append(themes, linkageTheme{ThemeID: themeID, Name: name, Source: "stock-strategy.limit-up-ladder", TradeDate: ladder.Current.TradeDate, Coverage: linkageCoverage{Scope: "ladder_attributed_stocks", Complete: false, IsSample: true, Source: "stock-strategy.limit-up-ladder", TradeDate: ladder.Current.TradeDate}, Members: []linkageMembership{}})
					}
					member := linkageMembership{Symbol: symbol, ThemeID: themeID, MembershipKind: kind, MembershipSource: source, EvidenceTime: ladder.Current.TradeDate, Confidence: confidence, Role: stock.ThemeLeaderRole}
					themes[index].Members = appendUniqueMembership(themes[index].Members, member)
				}
			}
		}
	}
	sort.Slice(themes, func(i, j int) bool { return themes[i].ThemeID < themes[j].ThemeID })
	return themes
}

func appendUniqueMembership(items []linkageMembership, candidate linkageMembership) []linkageMembership {
	for _, item := range items {
		if item.ThemeID == candidate.ThemeID && item.Symbol == candidate.Symbol && item.MembershipKind == candidate.MembershipKind {
			return items
		}
	}
	return append(items, candidate)
}

func canonicalLinkageSymbol(symbol string) string {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if len(symbol) == 9 && (strings.HasSuffix(symbol, ".SH") || strings.HasSuffix(symbol, ".SZ")) {
		for _, char := range symbol[:6] {
			if char < '0' || char > '9' {
				return ""
			}
		}
		return symbol
	}
	return ""
}

func shortHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:8])
}

func timeString(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func minTimeString(current, candidate string) string {
	if candidate == "" {
		return current
	}
	if current == "" {
		return candidate
	}
	left, leftErr := time.Parse(time.RFC3339Nano, current)
	right, rightErr := time.Parse(time.RFC3339Nano, candidate)
	if rightErr == nil && (leftErr != nil || right.Before(left)) {
		return candidate
	}
	return current
}

func deriveLinkageContextEvents(previousPayload []byte, current linkageContextSnapshot) []linkage.ContextEvent {
	if len(previousPayload) == 0 || current.TradeDate == "" {
		return nil
	}
	var previous linkageContextSnapshot
	if json.Unmarshal(previousPayload, &previous) != nil || previous.SchemaVersion != current.SchemaVersion {
		return nil
	}
	if previous.TradeDate != current.TradeDate {
		return nil
	}
	occurredAt := current.ReceivedAt
	snapshotIDs := []string{previous.SnapshotID, current.SnapshotID}
	events := make([]linkage.ContextEvent, 0)
	appendEvent := func(eventType, key string, payload any) {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return
		}
		seed, err := json.Marshal(struct {
			Type, TradeDate, Key, PreviousSnapshotID, CurrentSnapshotID string
			Payload                                                     json.RawMessage
		}{eventType, current.TradeDate, key, previous.SnapshotID, current.SnapshotID, encoded})
		if err != nil {
			return
		}
		digest := sha256.Sum256(seed)
		events = append(events, linkage.ContextEvent{EventID: hex.EncodeToString(digest[:]), EventType: eventType, OriginNode: current.Source, CausationID: current.SnapshotID, SourceSnapshotIDs: snapshotIDs, TradeDate: current.TradeDate, OccurredAt: occurredAt, Payload: encoded})
	}
	if sourceReadyForLinkageEvents(previous, "stock-strategy.themes") &&
		sourceReadyForLinkageEvents(current, "stock-strategy.themes") {
		oldThemes := map[string]linkageTheme{}
		for _, theme := range previous.Themes {
			oldThemes[theme.ThemeID] = theme
		}
		for _, theme := range current.Themes {
			if old, ok := oldThemes[theme.ThemeID]; !ok {
				if theme.TrendStage != "" {
					appendEvent("theme.phase_changed", theme.ThemeID, map[string]any{"theme_id": theme.ThemeID, "from": nil, "to": theme.TrendStage})
				}
			} else if old.TrendStage != theme.TrendStage {
				appendEvent("theme.phase_changed", theme.ThemeID, map[string]any{"theme_id": theme.ThemeID, "from": old.TrendStage, "to": theme.TrendStage})
			}
			if old, ok := oldThemes[theme.ThemeID]; ok && themeHeatChanged(old, theme) {
				appendEvent("theme.heat_changed", theme.ThemeID, map[string]any{
					"theme_id": theme.ThemeID, "source": theme.Source,
					"from_change_percent": old.ChangePercent, "to_change_percent": theme.ChangePercent,
					"from_trend_score": old.TrendScore, "to_trend_score": theme.TrendScore,
					"from_daily_strength_score": old.DailyStrengthScore, "to_daily_strength_score": theme.DailyStrengthScore,
					"from_five_day_strength_score": old.FiveDayStrengthScore, "to_five_day_strength_score": theme.FiveDayStrengthScore,
				})
			}
		}
	}
	if sourceReadyForLinkageEvents(previous, "stock-strategy.heat") &&
		sourceReadyForLinkageEvents(current, "stock-strategy.heat") {
		oldRanks := contextRanks(previous.Heat.Stocks)
		newRanks := contextRanks(current.Heat.Stocks)
		for key, next := range newRanks {
			if old, ok := oldRanks[key]; ok && old.Rank != next.Rank {
				appendEvent("heat.rank_changed", key, map[string]any{"symbol": next.Symbol, "source": next.Source, "from_rank": old.Rank, "to_rank": next.Rank, "scope": "top_100_per_source"})
			} else if !ok {
				appendEvent("heat.rank_changed", key, map[string]any{"symbol": next.Symbol, "source": next.Source, "from_rank": nil, "to_rank": next.Rank, "scope": "entered_top_100_observation_range"})
			}
		}
		for key, old := range oldRanks {
			if _, ok := newRanks[key]; !ok {
				appendEvent("heat.rank_changed", key, map[string]any{"symbol": old.Symbol, "source": old.Source, "from_rank": old.Rank, "to_rank": nil, "scope": "left_top_100_observation_range"})
			}
		}
	}
	if sourceReadyForLinkageEvents(previous, "stock-strategy.limit-up-ladder") &&
		sourceReadyForLinkageEvents(current, "stock-strategy.limit-up-ladder") {
		oldRoles := contextRoles(previous.Themes)
		newRoles := contextRoles(current.Themes)
		for key, next := range newRoles {
			if old, ok := oldRoles[key]; !ok {
				appendEvent("leader.role_changed", key, map[string]any{"symbol": next.Symbol, "theme_id": next.ThemeID, "from": nil, "to": next.Role, "source": next.Source})
			} else if old.Role != next.Role {
				appendEvent("leader.role_changed", key, map[string]any{"symbol": next.Symbol, "theme_id": next.ThemeID, "from": old.Role, "to": next.Role, "source": next.Source})
			}
		}
		for key, old := range oldRoles {
			if _, ok := newRoles[key]; !ok {
				appendEvent("leader.role_changed", key, map[string]any{"symbol": old.Symbol, "theme_id": old.ThemeID, "from": old.Role, "to": nil, "source": old.Source})
			}
		}
	}
	previousATSLeaders, previousATSReady := atsLeaderEvidenceRoles(previous)
	currentATSLeaders, currentATSReady := atsLeaderEvidenceRoles(current)
	if currentATSReady {
		keys := make([]string, 0, len(currentATSLeaders))
		for key := range currentATSLeaders {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			next := currentATSLeaders[key]
			old, exists := previousATSLeaders[key]
			if !previousATSReady || !exists {
				appendEvent("leader.role_changed", "ats.leader|"+key, map[string]any{
					"symbol": next.Symbol, "sector": next.ThemeID, "theme_id": next.ThemeID,
					"from": nil, "to": next.Role, "source": "ats.leader",
				})
			} else if old.Role != next.Role {
				appendEvent("leader.role_changed", "ats.leader|"+key, map[string]any{
					"symbol": next.Symbol, "sector": next.ThemeID, "theme_id": next.ThemeID,
					"from": old.Role, "to": next.Role, "source": "ats.leader",
				})
			}
		}
	}
	oldStatuses := map[string]string{}
	for _, source := range previous.Sources {
		oldStatuses[source.ID] = source.Status + ":" + source.Freshness
	}
	newStatuses := map[string]string{}
	for _, source := range current.Sources {
		newStatuses[source.ID] = source.Status + ":" + source.Freshness
		if old, ok := oldStatuses[source.ID]; !ok {
			appendEvent("data.status_changed", source.ID, map[string]any{"source": source.ID, "from": nil, "to": source.Status + ":" + source.Freshness, "error": source.Error})
		} else if old != source.Status+":"+source.Freshness {
			appendEvent("data.status_changed", source.ID, map[string]any{"source": source.ID, "from": old, "to": source.Status + ":" + source.Freshness, "error": source.Error})
		}
	}
	for sourceID, old := range oldStatuses {
		if _, ok := newStatuses[sourceID]; !ok {
			appendEvent("data.status_changed", sourceID, map[string]any{"source": sourceID, "from": old, "to": nil})
		}
	}
	oldEvidence := map[string]linkageContextEvidence{}
	for _, evidence := range previous.Evidence {
		oldEvidence[evidence.SourceID] = evidence
	}
	for _, evidence := range current.Evidence {
		if evidence.SourceID != "tk.auction" || evidence.TradeDate != current.TradeDate || evidence.Freshness != "fresh" {
			continue
		}
		old, exists := oldEvidence[evidence.SourceID]
		if exists && old.SnapshotID == evidence.SnapshotID {
			continue
		}
		var payload map[string]any
		if json.Unmarshal(evidence.Payload, &payload) != nil {
			continue
		}
		appendEvent("auction.confirmed", evidence.SourceID+"|"+evidence.SnapshotID, map[string]any{
			"source": evidence.SourceID, "snapshot_id": evidence.SnapshotID,
			"trade_date": evidence.TradeDate, "as_of": evidence.AsOf,
			"state": payload["state"], "candidate_count": payload["candidate_count"],
			"candidate_codes": payload["candidate_codes"],
		})
	}
	previousIPOSignals := map[string]bool{}
	for _, evidence := range previous.Evidence {
		if evidence.SourceID != "ats.ipo" {
			continue
		}
		var payload struct {
			Signals []map[string]any `json:"signals"`
		}
		if json.Unmarshal(evidence.Payload, &payload) != nil {
			continue
		}
		for _, signal := range payload.Signals {
			if signalID, ok := signal["signal_id"].(string); ok && signalID != "" {
				previousIPOSignals[signalID] = true
			}
		}
	}
	for _, evidence := range current.Evidence {
		if evidence.SourceID != "ats.ipo" || evidence.TradeDate != current.TradeDate || evidence.Freshness != "fresh" {
			continue
		}
		var payload struct {
			Signals []map[string]any `json:"signals"`
		}
		if json.Unmarshal(evidence.Payload, &payload) != nil {
			continue
		}
		for _, signal := range payload.Signals {
			signalID, ok := signal["signal_id"].(string)
			if !ok || signalID == "" || previousIPOSignals[signalID] {
				continue
			}
			appendEvent("ipo.signal_observed", evidence.SourceID+"|"+signalID, signal)
		}
	}
	previousAnomalyEvents := map[string]bool{}
	for _, evidence := range previous.Evidence {
		if evidence.SourceID != "tk.anomaly" {
			continue
		}
		var payload struct {
			Events []map[string]any `json:"events"`
		}
		if json.Unmarshal(evidence.Payload, &payload) != nil {
			continue
		}
		for _, event := range payload.Events {
			if eventID, ok := event["event_id"].(string); ok && eventID != "" {
				previousAnomalyEvents[eventID] = true
			}
		}
	}
	for _, evidence := range current.Evidence {
		if evidence.SourceID != "tk.anomaly" || evidence.TradeDate != current.TradeDate || evidence.Freshness != "fresh" {
			continue
		}
		var payload struct {
			Events []map[string]any `json:"events"`
		}
		if json.Unmarshal(evidence.Payload, &payload) != nil {
			continue
		}
		for _, event := range payload.Events {
			eventID, ok := event["event_id"].(string)
			tradeDate, dated := event["trade_date"].(string)
			if !ok || eventID == "" || !dated || tradeDate != current.TradeDate || previousAnomalyEvents[eventID] {
				continue
			}
			appendEvent("anomaly.detected", eventID, event)
		}
	}
	return events
}

func sourceReadyForLinkageEvents(snapshot linkageContextSnapshot, sourceID string) bool {
	for _, source := range snapshot.Sources {
		if source.ID == sourceID {
			return source.Status == "available" && source.Freshness == "fresh" &&
				(source.TradeDate == "" || source.TradeDate == snapshot.TradeDate)
		}
	}
	return false
}

func themeHeatChanged(previous, current linkageTheme) bool {
	return (previous.ChangePercent != 0 && current.ChangePercent != 0 && previous.ChangePercent != current.ChangePercent) ||
		(previous.TrendScore != 0 && current.TrendScore != 0 && previous.TrendScore != current.TrendScore) ||
		(previous.DailyStrengthScore != 0 && current.DailyStrengthScore != 0 && previous.DailyStrengthScore != current.DailyStrengthScore) ||
		(previous.FiveDayStrengthScore != 0 && current.FiveDayStrengthScore != 0 && previous.FiveDayStrengthScore != current.FiveDayStrengthScore)
}

type contextRank struct {
	Symbol, Source string
	Rank           int
}

func contextRanks(stocks []linkageHeatStock) map[string]contextRank {
	ranks := map[string]contextRank{}
	for _, stock := range stocks {
		for source, rank := range stock.Ranks {
			key := stock.Symbol + "|" + source
			ranks[key] = contextRank{Symbol: stock.Symbol, Source: source, Rank: rank}
		}
	}
	return ranks
}

type contextRole struct{ Symbol, ThemeID, Source, Role string }

func atsLeaderEvidenceRoles(snapshot linkageContextSnapshot) (map[string]contextRole, bool) {
	for _, evidence := range snapshot.Evidence {
		if evidence.SourceID != "ats.leader" {
			continue
		}
		if evidence.TradeDate != snapshot.TradeDate || evidence.Freshness != "fresh" {
			return nil, false
		}
		var payload struct {
			SourceFreshness string `json:"source_freshness"`
			Roles           []struct {
				Symbol string `json:"symbol"`
				Role   string `json:"role"`
				Sector string `json:"sector"`
			} `json:"roles"`
		}
		if json.Unmarshal(evidence.Payload, &payload) != nil || payload.SourceFreshness != "fresh" {
			return nil, false
		}
		roles := make(map[string]contextRole, len(payload.Roles))
		for _, item := range payload.Roles {
			symbol := canonicalLinkageSymbol(item.Symbol)
			role := strings.TrimSpace(item.Role)
			sector := strings.TrimSpace(item.Sector)
			if symbol == "" || role == "" {
				continue
			}
			key := symbol + "|" + sector
			roles[key] = contextRole{Symbol: symbol, ThemeID: sector, Source: "ats.leader", Role: role}
		}
		return roles, true
	}
	return nil, false
}

func contextRoles(themes []linkageTheme) map[string]contextRole {
	roles := map[string]contextRole{}
	for _, theme := range themes {
		for _, member := range theme.Members {
			if member.Role == "" {
				continue
			}
			key := theme.ThemeID + "|" + member.Symbol
			roles[key] = contextRole{Symbol: member.Symbol, ThemeID: theme.ThemeID, Source: member.MembershipSource, Role: member.Role}
		}
	}
	return roles
}
