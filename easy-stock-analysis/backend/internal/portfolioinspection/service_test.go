package portfolioinspection

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"easy-stock/backend/internal/stockanalysis"
)

func TestDesktopAdapterRunsInspectionWithoutHermes(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	service := NewService(store, nil, func(context.Context, string) (stockanalysis.Analysis, error) {
		return stockanalysis.Analysis{}, nil
	}, nil)
	validated, analyzed, aggregated := false, false, false
	service.SetDesktopAdapter(DesktopAdapter{
		Validate: func(_ context.Context, request Request) error {
			validated = request.AIProvider == "codex_desktop" && request.DesktopNodeID == "codex-node"
			return nil
		},
		AnalyzeHolding: func(_ context.Context, request Request, holding Holding) (stockanalysis.Analysis, error) {
			analyzed = request.Model == "gpt-6-luna" && holding.Symbol == "600519.SH"
			return stockanalysis.Analysis{AnalysisID: "research-1", Symbol: holding.Symbol, Name: "贵州茅台", AI: stockanalysis.AISynthesisStatus{Status: "ready"}}, nil
		},
		GenerateReport: func(_ context.Context, request Request, _ string) (string, error) {
			aggregated = request.ReasoningEffort == "high"
			return `{"executive_summary":"桌面桥已完成组合分析","risk_level":"中","style_match":"匹配"}`, nil
		},
	})

	created, err := service.Start(context.Background(), Request{
		TraderProfile: ProfileBalanced,
		AIProvider:    "codex_desktop", Model: "gpt-6-luna", ReasoningEffort: "high",
		DesktopNodeID: "codex-node", DesktopWorkspaceID: "workspace-a",
		Holdings: []Holding{{Symbol: "600519", Weight: 60}},
	})
	if err != nil {
		t.Fatalf("desktop runtime should not require Hermes: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, getErr := service.Get(context.Background(), created.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if job.Status != "running" {
			if job.Status != "succeeded" || job.Report == nil || job.Report.Conclusion.Source != "codex-desktop-ai" {
				t.Fatalf("unexpected desktop inspection result: status=%s report=%+v error=%s", job.Status, job.Report, job.Error)
			}
			if !validated || !analyzed || !aggregated {
				t.Fatalf("desktop adapter stages not all used: validate=%t analyze=%t aggregate=%t", validated, analyzed, aggregated)
			}
			if job.Request.AIProvider != "codex_desktop" || job.Request.DesktopWorkspaceID != "workspace-a" {
				t.Fatalf("runtime selection not persisted: %+v", job.Request)
			}
			encoded, marshalErr := json.Marshal(job.Request)
			if marshalErr != nil || !json.Valid(encoded) {
				t.Fatalf("persisted request is not valid JSON: %v", marshalErr)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("desktop inspection did not complete")
}

func TestNormalizeRequestPersistsSelectedDesktopRuntime(t *testing.T) {
	request, err := normalizeRequest(Request{
		TraderProfile: ProfileSteady,
		AIProvider:    "antigravity_desktop", Model: "gemini-3.8-flash-high", ReasoningEffort: "high",
		DesktopNodeID: "agv-node", DesktopWorkspaceID: "workspace-b",
		PreviousInspectionID: "hist-job-888",
		Holdings: []Holding{{Symbol: "600519", Weight: 50}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.AIProvider != "antigravity_desktop" || request.Model != "gemini-3.8-flash-high" || request.ReasoningEffort != "high" || request.DesktopNodeID != "agv-node" || request.DesktopWorkspaceID != "workspace-b" || request.PreviousInspectionID != "hist-job-888" {
		t.Fatalf("runtime selection or previous id was not preserved: %+v", request)
	}
}

func TestInspectionInheritsPreviousBaseline(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// 保存一条历史已完成巡检记录
	histCompleted := time.Now().UTC().Add(-24 * time.Hour)
	histJob := Job{
		ID:        "hist-job-1",
		Status:    "succeeded",
		Stage:     "completed",
		StartedAt: histCompleted,
		UpdatedAt: histCompleted,
		Request: Request{
			TraderProfile: ProfileBalanced,
			AIProvider:    "hermes",
			Holdings:      []Holding{{Symbol: "600519.SH", Weight: 60}},
		},
		ReportAvailable: true,
		Report: &Report{
			ID:          "hist-job-1",
			GeneratedAt: histCompleted,
			Conclusion: AIReport{
				HealthScore:      85,
				RiskLevel:        "低",
				ExecutiveSummary: "历史评估：持仓稳定，以茅台为核心，观察放量突破",
				Holdings: []HoldingConclusion{
					{
						Symbol:         "600519.SH",
						PortfolioRole:  "核心",
						ActionPriority: "保持",
						Action:         "持股并关注1850突破",
						Confirmation:   "放量站稳1850",
						Invalidation:   "跌破1800",
						Conclusion:     "结构健康",
					},
				},
			},
		},
	}
	if _, err := store.Save(context.Background(), histJob); err != nil {
		t.Fatal(err)
	}

	service := NewService(store, nil, func(context.Context, string) (stockanalysis.Analysis, error) {
		return stockanalysis.Analysis{AnalysisID: "research-2", Symbol: "600519.SH", Name: "贵州茅台", AI: stockanalysis.AISynthesisStatus{Status: "ready"}}, nil
	}, nil)

	service.SetDesktopAdapter(DesktopAdapter{
		Validate: func(_ context.Context, request Request) error { return nil },
		AnalyzeHolding: func(_ context.Context, _ Request, holding Holding) (stockanalysis.Analysis, error) {
			return stockanalysis.Analysis{AnalysisID: "research-2", Symbol: holding.Symbol, Name: "贵州茅台", AI: stockanalysis.AISynthesisStatus{Status: "ready"}}, nil
		},
		GenerateReport: func(_ context.Context, _ Request, _ string) (string, error) {
			return `{"executive_summary":"新交易日走势良好，茅台突破确认","risk_level":"低","style_match":"匹配","audit_summary":"对比前次巡检，茅台放量站稳1850兑现确认条件"}`, nil
		},
	})

	newJob, err := service.Start(context.Background(), Request{
		TraderProfile:        ProfileBalanced,
		AIProvider:           "codex_desktop",
		Model:                "gpt-6-luna",
		ReasoningEffort:      "high",
		DesktopNodeID:        "node-1",
		DesktopWorkspaceID:   "ws-1",
		PreviousInspectionID: "hist-job-1",
		Holdings:             []Holding{{Symbol: "600519.SH", Weight: 60}},
	})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, getErr := service.Get(context.Background(), newJob.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if job.Status == "succeeded" {
			if job.Report == nil {
				t.Fatal("expected report to be present")
			}
			if job.Report.PreviousInspectionID != "hist-job-1" {
				t.Fatalf("expected PreviousInspectionID to be 'hist-job-1', got %q", job.Report.PreviousInspectionID)
			}
			if job.Report.AuditBaseDate == "" {
				t.Fatal("expected AuditBaseDate to be populated")
			}
			if job.Report.Conclusion.AuditSummary == "" {
				t.Fatal("expected AuditSummary to be populated")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("inspection with baseline did not complete")
}
