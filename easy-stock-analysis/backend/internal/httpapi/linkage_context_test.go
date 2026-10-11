package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"easy-stock/backend/internal/foundation"
	"easy-stock/backend/internal/linkage"
)

func TestNormalizeLinkageThemesKeepsProviderIdentityAndSampleCoverage(t *testing.T) {
	themes := normalizeLinkageThemes([]foundation.ThemeOverview{{
		Theme: "BK0421", Name: "储能", Aliases: []string{"新型储能"}, Source: "ths",
		TradeDate: "2026-10-10", TotalNodes: 10, ChangePercent: 2.4, TrendScore: 72,
		LeaderStocks: []foundation.BoardStock{{Symbol: "600000.SH", Name: "浦发银行"}},
	}}, "2026-10-10")
	if len(themes) != 1 {
		t.Fatalf("theme count = %d, want 1", len(themes))
	}
	if themes[0].ThemeID != "provider:ths:BK0421" || themes[0].Source != "ths" {
		t.Fatalf("provider identity was not preserved: %+v", themes[0])
	}
	if themes[0].ChangePercent != 2.4 || themes[0].TrendScore != 72 {
		t.Fatalf("theme heat trajectory fields were dropped: %+v", themes[0])
	}
	if themes[0].Coverage.Complete || !themes[0].Coverage.IsSample || themes[0].Coverage.ObservedCount != 1 {
		t.Fatalf("leader sample was incorrectly marked complete: %+v", themes[0].Coverage)
	}
	if len(themes[0].Members) != 1 || themes[0].Members[0].MembershipKind != "leader_sample" {
		t.Fatalf("leader sample membership missing: %+v", themes[0].Members)
	}
}

func TestMergeLinkageEvidencePreservesStaleATSLeaderState(t *testing.T) {
	snapshot := linkageContextSnapshot{TradeDate: "2026-10-10", Sources: []linkageContextSource{}}
	evidence := []linkage.ContextEvidence{{
		WorkspaceID: "desk", NodeID: "ats-1", SourceID: "ats.leader", SnapshotID: "leader-1",
		TradeDate: "2026-10-10", AsOf: "2026-10-10T01:30:00Z", ReceivedAt: "2026-10-10T01:30:01Z",
		Payload: json.RawMessage(`{"schema_version":1,"source_id":"ats.leader","source_freshness":"stale","roles":[]}`),
	}}

	merged := mergeLinkageContextEvidence(snapshot, evidence)
	if len(merged.Evidence) != 1 || merged.Evidence[0].Freshness != "stale" {
		t.Fatalf("leader evidence freshness = %+v", merged.Evidence)
	}
	if len(merged.Sources) != 1 || merged.Sources[0].Freshness != "stale" {
		t.Fatalf("leader source freshness = %+v", merged.Sources)
	}
}

func TestMergeLinkageEvidenceKeepsAuctionHistoricalAfterSession(t *testing.T) {
	snapshot := linkageContextSnapshot{TradeDate: "2026-10-10", Sources: []linkageContextSource{}}
	evidence := []linkage.ContextEvidence{{
		WorkspaceID: "desk", NodeID: "tk-1", SourceID: "tk.auction", SnapshotID: "auction-1",
		TradeDate: "2026-10-10", AsOf: "2026-10-10T01:25:00Z", ReceivedAt: "2026-10-10T01:25:01Z",
		Payload: json.RawMessage(`{"schema_version":1,"source_id":"tk.auction","state":"NEUTRAL"}`),
	}}

	morning := time.Date(2026, 10, 10, 1, 26, 0, 0, time.UTC)
	merged := mergeLinkageContextEvidenceAt(snapshot, evidence, morning)
	if len(merged.Evidence) != 1 || merged.Evidence[0].Freshness != "fresh" {
		t.Fatalf("auction during opening session freshness = %+v", merged.Evidence)
	}
	afternoon := time.Date(2026, 10, 10, 6, 0, 0, 0, time.UTC)
	merged = mergeLinkageContextEvidenceAt(snapshot, evidence, afternoon)
	if len(merged.Evidence) != 1 || merged.Evidence[0].Freshness != "historical" {
		t.Fatalf("same-day afternoon auction evidence freshness = %+v", merged.Evidence)
	}
}

func TestATSLeaderEvidenceUsesSourceAgeAndRejectsUnknownSources(t *testing.T) {
	now := time.Date(2026, 10, 10, 1, 30, 0, 0, time.UTC)
	freshPayload := json.RawMessage(`{"source_freshness":"fresh"}`)
	item := linkage.ContextEvidence{SourceID: "ats.leader", TradeDate: "2026-10-10",
		AsOf: now.Add(-45 * time.Second).Format(time.RFC3339Nano), Payload: freshPayload}
	if got := linkageEvidenceFreshness(item, "2026-10-10", now); got != "fresh" {
		t.Fatalf("45-second ATS evidence freshness = %q, want fresh", got)
	}
	item.AsOf = now.Add(-91 * time.Second).Format(time.RFC3339Nano)
	if got := linkageEvidenceFreshness(item, "2026-10-10", now); got != "stale" {
		t.Fatalf("91-second ATS evidence freshness = %q, want stale", got)
	}
	item.AsOf = now.Add(31 * time.Second).Format(time.RFC3339Nano)
	if got := linkageEvidenceFreshness(item, "2026-10-10", now); got != "stale" {
		t.Fatalf("future ATS evidence freshness = %q, want stale", got)
	}
	item.AsOf = "not-a-time"
	if got := linkageEvidenceFreshness(item, "2026-10-10", now); got != "unknown" {
		t.Fatalf("invalid ATS as_of freshness = %q, want unknown", got)
	}
	item.SourceID = "ats.ipo"
	item.AsOf = now.Add(-10 * time.Minute).Format(time.RFC3339Nano)
	if got := linkageEvidenceFreshness(item, "2026-10-10", now); got != "historical" {
		t.Fatalf("same-day IPO event freshness = %q, want historical", got)
	}
	item.AsOf = now.Add(31 * time.Second).Format(time.RFC3339Nano)
	if got := linkageEvidenceFreshness(item, "2026-10-10", now); got != "stale" {
		t.Fatalf("future IPO event freshness = %q, want stale", got)
	}
	item.SourceID = "other.unknown"
	item.AsOf = now.Format(time.RFC3339Nano)
	if got := linkageEvidenceFreshness(item, "2026-10-10", now); got != "unknown" {
		t.Fatalf("unrecognized evidence freshness = %q, want unknown", got)
	}
}

func TestLadderMembershipKeepsAllProviderIdentitiesForSharedThemeAlias(t *testing.T) {
	themes := normalizeLinkageThemes([]foundation.ThemeOverview{
		{Theme: "BK0421", Name: "储能", Aliases: []string{"新能源储能"}, Source: "ths"},
		{Theme: "BK0912", Name: "电力设备", Aliases: []string{"新能源储能"}, Source: "eastmoney"},
	}, "2026-10-10")
	ladder := limitUpLadderData{Current: limitUpLadderDay{TradeDate: "2026-10-10", Levels: []limitUpLadderLevel{{
		Level: 3, Stocks: []limitUpLadderStock{{Symbol: "600000.SH", PrimaryTheme: "新能源储能", ThemeSource: "tdx", ThemeConfidence: 0.8}},
	}}}}

	merged := mergeLadderThemeMemberships(themes, ladder)
	for _, themeID := range []string{"provider:ths:BK0421", "provider:eastmoney:BK0912"} {
		found := false
		for _, theme := range merged {
			if theme.ThemeID != themeID {
				continue
			}
			for _, member := range theme.Members {
				if member.Symbol == "600000.SH" && member.MembershipKind == "ladder_primary" {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("ladder membership lost provider theme identity %q: %+v", themeID, merged)
		}
	}
}

func TestDeriveLinkageContextEventsRecordsObservableChangesOnly(t *testing.T) {
	previous := linkageContextSnapshot{
		SchemaVersion: 1, Source: "stock-strategy.go", SnapshotID: "snapshot-1", TradeDate: "2026-10-10",
		ReceivedAt: "2026-10-10T01:00:00Z",
		Sources: []linkageContextSource{
			{ID: "stock-strategy.themes", Status: "available", Freshness: "fresh", TradeDate: "2026-10-10"},
			{ID: "stock-strategy.heat", Status: "available", Freshness: "fresh", TradeDate: "2026-10-10"},
			{ID: "stock-strategy.limit-up-ladder", Status: "available", Freshness: "fresh", TradeDate: "2026-10-10"},
			{ID: "tk.auction", Status: "unavailable", Freshness: "unknown"},
		},
		Themes: []linkageTheme{{ThemeID: "provider:ths:BK0421", TrendStage: "rising", ChangePercent: 1.1, TrendScore: 60, DailyStrengthScore: 58, FiveDayStrengthScore: 55, Members: []linkageMembership{{Symbol: "600000.SH", ThemeID: "provider:ths:BK0421", Role: "leader", MembershipSource: "ths"}}}},
		Heat:   linkageHeatSnapshot{Stocks: []linkageHeatStock{{Symbol: "600000.SH", Ranks: map[string]int{"ths": 12}}}},
	}
	previousJSON, err := json.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}

	current := previous
	current.SnapshotID = "snapshot-2"
	current.ReceivedAt = "2026-10-10T01:01:00Z"
	current.Sources = []linkageContextSource{
		{ID: "stock-strategy.themes", Status: "available", Freshness: "fresh", TradeDate: "2026-10-10"},
		{ID: "stock-strategy.heat", Status: "available", Freshness: "fresh", TradeDate: "2026-10-10"},
		{ID: "stock-strategy.limit-up-ladder", Status: "available", Freshness: "fresh", TradeDate: "2026-10-10"},
		{ID: "tk.auction", Status: "available", Freshness: "fresh", TradeDate: "2026-10-10"},
	}
	current.Themes = []linkageTheme{{ThemeID: "provider:ths:BK0421", TrendStage: "cooling", ChangePercent: 2.2, TrendScore: 70, DailyStrengthScore: 68, FiveDayStrengthScore: 64, Members: []linkageMembership{{Symbol: "600000.SH", ThemeID: "provider:ths:BK0421", Role: "core", MembershipSource: "ths"}}}}
	current.Heat = linkageHeatSnapshot{Stocks: []linkageHeatStock{{Symbol: "600000.SH", Ranks: map[string]int{"ths": 5}}}}
	first := deriveLinkageContextEvents(previousJSON, current)
	second := deriveLinkageContextEvents(previousJSON, current)
	if len(first) != 5 {
		t.Fatalf("event count = %d, want stock heat, theme heat, phase, role and data status changes", len(first))
	}
	ids := map[string]bool{}
	for _, event := range first {
		ids[event.EventID] = true
	}
	for _, event := range second {
		if !ids[event.EventID] {
			t.Fatalf("event id changed across recovery replay: %s", event.EventID)
		}
	}
	repeatedTransition := previous
	repeatedTransition.SnapshotID = "snapshot-3"
	repeatedJSON, err := json.Marshal(repeatedTransition)
	if err != nil {
		t.Fatal(err)
	}
	repeatedCurrent := current
	repeatedCurrent.SnapshotID = "snapshot-4"
	third := deriveLinkageContextEvents(repeatedJSON, repeatedCurrent)
	for _, event := range third {
		if ids[event.EventID] {
			t.Fatalf("a later occurrence reused an earlier event id: %s", event.EventID)
		}
	}
	unchanged := deriveLinkageContextEvents(previousJSON, previous)
	if len(unchanged) != 0 {
		t.Fatalf("unchanged snapshot produced %d events", len(unchanged))
	}
}

func TestDeriveLinkageContextEventsDoesNotTreatUnavailableSourcesAsExits(t *testing.T) {
	previous := linkageContextSnapshot{
		SchemaVersion: 1, SnapshotID: "snapshot-1", TradeDate: "2026-10-10",
		Sources: []linkageContextSource{
			{ID: "stock-strategy.themes", Status: "available", Freshness: "fresh", TradeDate: "2026-10-10"},
			{ID: "stock-strategy.heat", Status: "available", Freshness: "fresh", TradeDate: "2026-10-10"},
			{ID: "stock-strategy.limit-up-ladder", Status: "available", Freshness: "fresh", TradeDate: "2026-10-10"},
		},
		Themes: []linkageTheme{{ThemeID: "provider:ths:BK0421", TrendStage: "rising", Members: []linkageMembership{{
			Symbol: "600000.SH", ThemeID: "provider:ths:BK0421", Role: "leader", MembershipSource: "tdx",
		}}}},
		Heat: linkageHeatSnapshot{Stocks: []linkageHeatStock{{Symbol: "600000.SH", Ranks: map[string]int{"ths": 12}}}},
	}
	previousJSON, err := json.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}

	current := previous
	current.SnapshotID = "snapshot-2"
	current.Sources = []linkageContextSource{
		{ID: "stock-strategy.themes", Status: "unavailable", Freshness: "unknown", TradeDate: "2026-10-10"},
		{ID: "stock-strategy.heat", Status: "unavailable", Freshness: "unknown", TradeDate: "2026-10-10"},
		{ID: "stock-strategy.limit-up-ladder", Status: "unavailable", Freshness: "unknown", TradeDate: "2026-10-10"},
	}
	current.Themes = nil
	current.Heat = linkageHeatSnapshot{}
	events := deriveLinkageContextEvents(previousJSON, current)
	for _, event := range events {
		if event.EventType == "theme.phase_changed" || event.EventType == "theme.heat_changed" ||
			event.EventType == "heat.rank_changed" || event.EventType == "leader.role_changed" {
			t.Fatalf("unavailable data generated a semantic exit event: %+v", event)
		}
	}
}

func TestAuctionEvidenceProducesAnIdempotentConfirmationEvent(t *testing.T) {
	previous := linkageContextSnapshot{SchemaVersion: 1, SnapshotID: "context-1", TradeDate: "2026-10-10",
		Evidence: []linkageContextEvidence{{SourceID: "tk.auction", SnapshotID: "auction-1", TradeDate: "2026-10-10", Freshness: "fresh"}}}
	previousJSON, err := json.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	current := previous
	current.SnapshotID = "context-2"
	current.Evidence = []linkageContextEvidence{{SourceID: "tk.auction", SnapshotID: "auction-2", TradeDate: "2026-10-10",
		AsOf: "2026-10-10T01:25:00Z", Freshness: "fresh", Payload: json.RawMessage(`{"state":"REPAIR","candidate_count":1,"candidate_codes":["600000"]}`)}}
	first := deriveLinkageContextEvents(previousJSON, current)
	second := deriveLinkageContextEvents(previousJSON, current)
	if len(first) != 1 || first[0].EventType != "auction.confirmed" || first[0].EventID != second[0].EventID {
		t.Fatalf("auction events are not stable/idempotent: first=%+v second=%+v", first, second)
	}
	var payload map[string]any
	if err := json.Unmarshal(first[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["candidate_count"] != float64(1) || payload["state"] != "REPAIR" {
		t.Fatalf("auction event lost its evidence: %+v", payload)
	}
	current.TradeDate = "2026-10-11"
	if events := deriveLinkageContextEvents(previousJSON, current); len(events) != 0 {
		t.Fatalf("cross-session auction evidence created a confirmation event: %+v", events)
	}
	current.TradeDate = "2026-10-10"
	current.Evidence[0].Freshness = "stale"
	if events := deriveLinkageContextEvents(previousJSON, current); len(events) != 0 {
		t.Fatalf("stale auction evidence created a confirmation event: %+v", events)
	}
}

func TestATSLeaderEvidenceEmitsOnlyFreshRoleAddsAndChanges(t *testing.T) {
	previous := linkageContextSnapshot{SchemaVersion: 1, SnapshotID: "context-1", TradeDate: "2026-10-10",
		Evidence: []linkageContextEvidence{{SourceID: "ats.leader", SnapshotID: "ats-1", TradeDate: "2026-10-10", Freshness: "fresh",
			Payload: json.RawMessage(`{"source_freshness":"fresh","roles":[{"symbol":"600000.SH","role":"空间高度龙","sector":"储能"},{"symbol":"000001.SZ","role":"主线先锋","sector":"银行"}]}`)}}}
	previousJSON, err := json.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}

	current := previous
	current.SnapshotID = "context-2"
	current.Evidence = []linkageContextEvidence{{SourceID: "ats.leader", SnapshotID: "ats-2", TradeDate: "2026-10-10", Freshness: "fresh",
		Payload: json.RawMessage(`{"source_freshness":"fresh","roles":[{"symbol":"600000.SH","role":"趋势容量中军","sector":"储能"},{"symbol":"600001.SH","role":"补涨龙","sector":"储能"}]}`)}}
	first := deriveLinkageContextEvents(previousJSON, current)
	second := deriveLinkageContextEvents(previousJSON, current)
	if len(first) != 2 || first[0].EventType != "leader.role_changed" || first[0].EventID != second[0].EventID || first[1].EventID != second[1].EventID {
		t.Fatalf("fresh ATS role delta events are not stable: first=%+v second=%+v", first, second)
	}
	var payload map[string]any
	if err := json.Unmarshal(first[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["source"] != "ats.leader" || payload["from"] != "空间高度龙" || payload["to"] != "趋势容量中军" {
		t.Fatalf("ATS role change lost role/source evidence: %+v", payload)
	}

	unchanged := current
	unchanged.SnapshotID = "context-3"
	unchangedJSON, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if events := deriveLinkageContextEvents(unchangedJSON, unchanged); len(events) != 0 {
		t.Fatalf("unchanged ATS roles emitted %d events", len(events))
	}

	// The ATS list is a sample: a missing role must not be interpreted as a role exit.
	partial := current
	partial.SnapshotID = "context-4"
	partial.Evidence = []linkageContextEvidence{{SourceID: "ats.leader", SnapshotID: "ats-3", TradeDate: "2026-10-10", Freshness: "fresh",
		Payload: json.RawMessage(`{"source_freshness":"fresh","coverage":{"complete":false,"is_sample":true},"roles":[{"symbol":"600000.SH","role":"趋势容量中军","sector":"储能"}]}`)}}
	partialPrevious := current
	partialPrevious.Evidence = []linkageContextEvidence{{SourceID: "ats.leader", SnapshotID: "ats-2", TradeDate: "2026-10-10", Freshness: "fresh",
		Payload: json.RawMessage(`{"source_freshness":"fresh","coverage":{"complete":false,"is_sample":true},"roles":[{"symbol":"600000.SH","role":"趋势容量中军","sector":"储能"},{"symbol":"600001.SH","role":"补涨龙","sector":"储能"}]}`)}}
	partialPreviousJSON, err := json.Marshal(partialPrevious)
	if err != nil {
		t.Fatal(err)
	}
	if events := deriveLinkageContextEvents(partialPreviousJSON, partial); len(events) != 0 {
		t.Fatalf("sample omission generated role events: %+v", events)
	}

	stale := current
	stale.Evidence = []linkageContextEvidence{{SourceID: "ats.leader", SnapshotID: "ats-stale", TradeDate: "2026-10-10", Freshness: "stale",
		Payload: json.RawMessage(`{"source_freshness":"stale","roles":[{"symbol":"600000.SH","role":"已过期","sector":"储能"}]}`)}}
	for _, event := range deriveLinkageContextEvents(previousJSON, stale) {
		if event.EventType == "leader.role_changed" && string(event.Payload) != "" {
			t.Fatalf("stale ATS evidence emitted a role event: %+v", event)
		}
	}
}

func TestIPOSignalEvidenceEmitsOnlyNewDatedObservations(t *testing.T) {
	previous := linkageContextSnapshot{SchemaVersion: 1, SnapshotID: "context-1", TradeDate: "2026-10-10",
		Evidence: []linkageContextEvidence{{SourceID: "ats.ipo", SnapshotID: "ipo-1", TradeDate: "2026-10-10", Freshness: "fresh",
			Payload: json.RawMessage(`{"signals":[{"signal_id":"signal-1","symbol":"600000.SH"}]}`)}}}
	previousJSON, err := json.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	current := previous
	current.SnapshotID = "context-2"
	current.Evidence = []linkageContextEvidence{{SourceID: "ats.ipo", SnapshotID: "ipo-2", TradeDate: "2026-10-10", Freshness: "fresh",
		Payload: json.RawMessage(`{"signals":[{"signal_id":"signal-1","symbol":"600000.SH"},{"signal_id":"signal-2","symbol":"000001.SZ","signal_type":"BREAKOUT"}]}`)}}
	first := deriveLinkageContextEvents(previousJSON, current)
	second := deriveLinkageContextEvents(previousJSON, current)
	if len(first) != 1 || first[0].EventType != "ipo.signal_observed" || first[0].EventID != second[0].EventID {
		t.Fatalf("IPO evidence events are not delta/idempotent: first=%+v second=%+v", first, second)
	}
	current.Evidence[0].Payload = json.RawMessage(`{"signals":[{"signal_id":"signal-1","symbol":"600000.SH"}]}`)
	unchangedJSON, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	current.SnapshotID = "context-3"
	if events := deriveLinkageContextEvents(unchangedJSON, current); len(events) != 0 {
		t.Fatalf("repeated IPO evidence emitted %d duplicate events", len(events))
	}
	current.TradeDate = "2026-10-11"
	if events := deriveLinkageContextEvents(previousJSON, current); len(events) != 0 {
		t.Fatalf("cross-session IPO evidence emitted %d events", len(events))
	}
	current.TradeDate = "2026-10-10"
	current.Evidence[0].Freshness = "stale"
	if events := deriveLinkageContextEvents(previousJSON, current); len(events) != 0 {
		t.Fatalf("stale IPO evidence emitted %d events", len(events))
	}
}

func TestTKAnomalyEvidenceHistoryEmitsNewSameDayEventsOnly(t *testing.T) {
	previous := linkageContextSnapshot{SchemaVersion: 1, SnapshotID: "context-1", TradeDate: "2026-10-10",
		Evidence: []linkageContextEvidence{{SourceID: "tk.anomaly", SnapshotID: "anomaly-1", TradeDate: "2026-10-10", Freshness: "fresh",
			Payload: json.RawMessage(`{"events":[{"event_id":"event-1","event_type":"pattern","symbol":"600000.SH","trade_date":"2026-10-10"}]}`)}}}
	previousJSON, err := json.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	current := previous
	current.SnapshotID = "context-2"
	current.Evidence = []linkageContextEvidence{{SourceID: "tk.anomaly", SnapshotID: "anomaly-2", TradeDate: "2026-10-10", Freshness: "fresh",
		Payload: json.RawMessage(`{"events":[{"event_id":"event-1","event_type":"pattern","symbol":"600000.SH","trade_date":"2026-10-10"},{"event_id":"event-2","event_type":"risk","symbol":"000001.SZ","trade_date":"2026-10-10"},{"event_id":"event-old","symbol":"600000.SH","trade_date":"2026-10-09"}]}`)}}
	first := deriveLinkageContextEvents(previousJSON, current)
	second := deriveLinkageContextEvents(previousJSON, current)
	if len(first) != 1 || first[0].EventType != "anomaly.detected" || first[0].EventID != second[0].EventID {
		t.Fatalf("TK anomaly events are not new-only/idempotent: first=%+v second=%+v", first, second)
	}
	current.Evidence[0].Freshness = "stale"
	if events := deriveLinkageContextEvents(previousJSON, current); len(events) != 0 {
		t.Fatalf("stale TK anomaly evidence emitted %d events", len(events))
	}

	stored := []linkage.ContextEvidence{
		{SourceID: "tk.anomaly", SnapshotID: "anomaly-1", TradeDate: "2026-10-10", AsOf: "2026-10-10T01:00:00Z",
			Payload: json.RawMessage(`{"events":[{"event_id":"event-1","symbol":"600000.SH","trade_date":"2026-10-10"}]}`)},
		{SourceID: "tk.anomaly", SnapshotID: "anomaly-2", TradeDate: "2026-10-10", AsOf: "2026-10-10T01:01:00Z",
			Payload: json.RawMessage(`{"events":[{"event_id":"event-2","symbol":"000001.SZ","trade_date":"2026-10-10"}]}`)},
	}
	merged := mergeLinkageContextEvidence(linkageContextSnapshot{TradeDate: "2026-10-10", Sources: []linkageContextSource{}}, stored)
	if len(merged.Evidence) != 1 {
		t.Fatalf("merged evidence count = %d", len(merged.Evidence))
	}
	var aggregate struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(merged.Evidence[0].Payload, &aggregate); err != nil || len(aggregate.Events) != 2 {
		t.Fatalf("anomaly history was not retained: events=%+v err=%v", aggregate.Events, err)
	}
}

func TestMergeLinkageEvidencePreservesSameSourceFromDifferentNodes(t *testing.T) {
	snapshot := linkageContextSnapshot{TradeDate: "2026-10-11", Sources: []linkageContextSource{}}
	stored := []linkage.ContextEvidence{
		{WorkspaceID: "desk", NodeID: "tk-a", SourceID: "tk.auction", SnapshotID: "auction-a",
			TradeDate: "2026-10-11", AsOf: "2026-10-11T09:20:00+08:00", ReceivedAt: "2026-10-11T09:20:01+08:00",
			Payload: json.RawMessage("{\"state\":\"NEUTRAL\"}")},
		{WorkspaceID: "desk", NodeID: "tk-b", SourceID: "tk.auction", SnapshotID: "auction-b",
			TradeDate: "2026-10-11", AsOf: "2026-10-11T09:21:00+08:00", ReceivedAt: "2026-10-11T09:21:01+08:00",
			Payload: json.RawMessage("{\"state\":\"STRONG\"}")},
	}
	merged := mergeLinkageContextEvidence(snapshot, stored)
	if len(merged.Evidence) != 2 || merged.Evidence[0].NodeID != "tk-a" || merged.Evidence[1].NodeID != "tk-b" {
		t.Fatalf("same source evidence was collapsed across nodes: %+v", merged.Evidence)
	}
	if len(merged.Sources) != 2 || merged.Sources[0].ID == merged.Sources[1].ID {
		t.Fatalf("same source summaries lack stable node identities: %+v", merged.Sources)
	}
}

func TestLinkageContextWorkspaceRequiresConfiguredPeerOrWorkspace(t *testing.T) {
	service, err := linkage.NewService(filepath.Join(t.TempDir(), "linkage.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	request := httptest.NewRequest("GET", "/api/v1/linkage/context", nil)
	if workspace, ok := linkageContextWorkspace(request, service); !ok || workspace != "" {
		t.Fatalf("public unscoped request should remain valid: workspace=%q ok=%v", workspace, ok)
	}
	request = httptest.NewRequest("GET", "/api/v1/linkage/context?workspace_id=foreign", nil)
	if _, ok := linkageContextWorkspace(request, service); ok {
		t.Fatal("unknown workspace was accepted")
	}
}

func TestATSLeaderEventsRemainDistinctAcrossNodes(t *testing.T) {
	previous := linkageContextSnapshot{SchemaVersion: 1, SnapshotID: "context-1", TradeDate: "2026-10-11",
		Evidence: []linkageContextEvidence{
			{SourceID: "ats.leader", NodeID: "ats-a", SnapshotID: "a-1", TradeDate: "2026-10-11", Freshness: "fresh",
				Payload: json.RawMessage("{\"source_freshness\":\"fresh\",\"roles\":[{\"symbol\":\"600000.SH\",\"role\":\"leader\",\"sector\":\"bank\"}]}")},
			{SourceID: "ats.leader", NodeID: "ats-b", SnapshotID: "b-1", TradeDate: "2026-10-11", Freshness: "fresh",
				Payload: json.RawMessage("{\"source_freshness\":\"fresh\",\"roles\":[{\"symbol\":\"600000.SH\",\"role\":\"candidate\",\"sector\":\"bank\"}]}")},
		}}
	previousJSON, err := json.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	current := previous
	current.SnapshotID = "context-2"
	current.Evidence = append([]linkageContextEvidence(nil), previous.Evidence...)
	for i := range current.Evidence {
		current.Evidence[i].SnapshotID += "-next"
	}
	current.Evidence[0].Payload = json.RawMessage("{\"source_freshness\":\"fresh\",\"roles\":[{\"symbol\":\"600000.SH\",\"role\":\"core_leader\",\"sector\":\"bank\"}]}")
	current.Evidence[1].Payload = json.RawMessage("{\"source_freshness\":\"fresh\",\"roles\":[{\"symbol\":\"600000.SH\",\"role\":\"rising\",\"sector\":\"bank\"}]}")
	events := deriveLinkageContextEvents(previousJSON, current)
	seen := map[string]bool{}
	for _, event := range events {
		if event.EventType != "leader.role_changed" {
			continue
		}
		var payload map[string]string
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		seen[payload["node_id"]] = true
	}
	if len(seen) != 2 || !seen["ats-a"] || !seen["ats-b"] {
		t.Fatalf("same-source leader transitions collapsed across nodes: events=%+v", events)
	}
}
