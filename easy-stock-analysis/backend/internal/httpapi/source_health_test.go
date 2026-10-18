package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type sourceHealthRoundTrip func(*http.Request) (*http.Response, error)

func (f sourceHealthRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSourceHealthRequiresObservedRequestsAndExpires(t *testing.T) {
	cacheState := ""
	monitor := newSourceHealthMonitor(sourceHealthRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"X-Stock-Cache": []string{cacheState}}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}))
	find := func() string {
		for _, item := range monitor.snapshot(time.Now()) {
			if item.ID == "eastmoney" {
				return item.Status
			}
		}
		return "missing"
	}
	if find() != "unknown" {
		t.Fatal("unvisited source reported healthy")
	}
	request, _ := http.NewRequest("GET", "https://push2.eastmoney.com/data", nil)
	response, err := monitor.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if find() != "ok" {
		t.Fatal(find())
	}
	cacheState = "STALE"
	response, _ = monitor.RoundTrip(request)
	response.Body.Close()
	for _, item := range monitor.snapshot(time.Now()) {
		if item.ID == "eastmoney" && (item.OK || item.Status != "stale") {
			t.Fatalf("stale fallback claimed healthy: %+v", item)
		}
	}
	for _, item := range monitor.snapshot(time.Now().Add(6 * time.Minute)) {
		if item.ID == "eastmoney" && item.Status != "unknown" {
			t.Fatal(item.Status)
		}
	}
	if observedSource("eastmoney.com.attacker.example") != "" {
		t.Fatal("hostname suffix was not checked")
	}
}
func TestSourceHealthRecordsNetworkFailure(t *testing.T) {
	monitor := newSourceHealthMonitor(sourceHealthRoundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("network failure") }))
	request, _ := http.NewRequest("GET", "https://qt.gtimg.cn/q=test", nil)
	_, _ = monitor.RoundTrip(request)
	for _, item := range monitor.snapshot(time.Now()) {
		if item.ID == "tencent" && (item.OK || item.Status != "error") {
			t.Fatalf("%+v", item)
		}
	}
}
func TestAppendSuccessCountersAreNotFailures(t *testing.T) {
	if appendLogHasFailure("CRYPTO append finished: symbols=3 bars=15 failures=0 expected=2026-10-17") {
		t.Fatal("successful counter marked as failure")
	}
	if !appendLogHasFailure("CRYPTO append finished: symbols=3 bars=10 failures=1") {
		t.Fatal("failure counter ignored")
	}
	if !appendLogHasFailure("HTTP Error 503 failures=0") {
		t.Fatal("actual error ignored")
	}
	schedules := parseMarketAppendSchedules("CRON_TZ=Asia/Shanghai\n20 17 * * 1-5 root /project/market-append.sh cn-retry\n")
	if len(schedules) != 1 || !strings.HasSuffix(schedules[0].logPath, "market-append-cn.log") {
		t.Fatalf("retry log missing: %+v", schedules)
	}
}
