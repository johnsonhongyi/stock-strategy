package portfolioinspection

import (
	"strings"
	"testing"
)

func TestBuildPromptPinsDeterministicV2Scores(t *testing.T) {
	rules, _ := RulesFor(ProfileBalanced)
	metrics := Metrics{
		TotalPositionPercent: 70, CashPercent: 30, CoveragePercent: 100,
		HealthScore: 78, HealthScoreAvailable: true, WeightedRisk: 35,
		RiskResilienceScore: 100, DiversificationScore: 72, StyleMatchScore: 100,
	}
	prompt, err := buildPrompt(Request{TraderProfile: ProfileBalanced}, nil, metrics, rules, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`"prompt_version":"portfolio-inspection-v2"`,
		`"deterministic_summary":{"health_score":78,"risk_level":"低","style_match":"匹配"}`,
		"必须原样复制deterministic_summary",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt missing %q: %s", expected, prompt)
		}
	}
}

func TestBuildPromptWithPreviousInspectionBaseline(t *testing.T) {
	rules, _ := RulesFor(ProfileAggressive)
	metrics := Metrics{
		TotalPositionPercent: 80, CashPercent: 20, CoveragePercent: 100,
		HealthScore: 82, HealthScoreAvailable: true, WeightedRisk: 40,
		RiskResilienceScore: 85, DiversificationScore: 65, StyleMatchScore: 90,
	}
	baseline := &PreviousInspectionBaseline{
		InspectionID:     "prev-inspection-123",
		GeneratedAt:      "2026-10-21 15:30",
		HealthScore:      80,
		RiskLevel:        "中",
		ExecutiveSummary: "前次评估：持仓偏激进，重点关注龙头股突破确认",
		Holdings: map[string]PreviousHoldingBaseline{
			"600519.SH": {
				Symbol:         "600519.SH",
				PortfolioRole:  "核心",
				ActionPriority: "保持",
				Action:         "持股观察突破1850",
				Confirmation:   "放量站稳1850",
				Invalidation:   "跌破1800",
			},
		},
	}
	prompt, err := buildPrompt(Request{TraderProfile: ProfileAggressive, PreviousInspectionID: "prev-inspection-123"}, nil, metrics, rules, baseline)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"previous_inspection_baseline",
		"prev-inspection-123",
		"走势审核与再次更新巡检",
		"trend_audit",
		"audit_summary",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt missing baseline expectation %q: %s", expected, prompt)
		}
	}
}
