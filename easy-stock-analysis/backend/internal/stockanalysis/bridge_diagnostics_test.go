package stockanalysis

import (
	"context"
	"easy-stock/backend/internal/hermes"
	"testing"
)

type diagnosticPrompter struct{}

func (diagnosticPrompter) Prompt(context.Context, string) (hermes.PromptResult, error) {
	return hermes.PromptResult{Content: `{"ok":true}`, BridgeDiagnostics: &hermes.BridgeDiagnostics{Timings: map[string]int64{"queue_wait_ms": 3}, Tokens: map[string]int64{"inputTokens": 14}}}, nil
}
func TestPromptDiagnosticsReachResearchAttempt(t *testing.T) {
	var diagnostic promptJSONAttempt
	_, err := promptJSONObjectWithOptions[map[string]any](context.Background(), diagnosticPrompter{}, "test", "test", promptJSONObjectOptions{maxAttempts: 1, onAttempt: func(attempt promptJSONAttempt) { diagnostic = attempt }})
	if err != nil || diagnostic.bridgeTimings["queue_wait_ms"] != 3 || diagnostic.diagnosticTokens["inputTokens"] != 14 {
		t.Fatal(diagnostic, err)
	}
}
