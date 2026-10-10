package marketcache

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

type testRoundTripper func(*http.Request) (*http.Response, error)

func (f testRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestForcedRefreshNeverReturnsStale(t *testing.T) {
	c, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	failing := false
	tr := c.RoundTripper(testRoundTripper(func(r *http.Request) (*http.Response, error) {
		if failing {
			return nil, errors.New("connection refused")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("fresh")), Request: r}, nil
	}))
	req, _ := http.NewRequest("GET", "https://api.kraken.com/0/public/Ticker?pair=XBTUSD", nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	failing = true
	forced := req.WithContext(WithRefresh(context.Background()))
	if resp, err := tr.RoundTrip(forced); err == nil {
		resp.Body.Close()
		t.Fatal("forced refresh returned cache instead of error")
	}
	resp, err = tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "fresh" {
		t.Fatalf("cache lost: %s", body)
	}
}


func TestSuccessfulHotFetchEnforcesMinimumInterval(t *testing.T) {
	cache, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	calls := 0
	transport := cache.RoundTripper(testRoundTripper(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader("fresh")), Request: request}, nil
	}))
	request, _ := http.NewRequest(http.MethodGet, "https://eq.10jqka.com.cn/open/api/hot_list/v1/hot_stock/a/hour/data.txt", nil)
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	forced := request.WithContext(WithRefresh(context.Background()))
	response, err = transport.RoundTrip(forced)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("forced refresh status=%d, want 429", response.StatusCode)
	}
	retryAfter, err := strconv.Atoi(response.Header.Get("Retry-After"))
	if err != nil || retryAfter < 1700 {
		t.Fatalf("unexpected retry-after: %q (%v)", response.Header.Get("Retry-After"), err)
	}
	if calls != 1 {
		t.Fatalf("upstream called %d times while interval was active", calls)
	}
}

func TestBoardFetchCooldownIsScopedPerBoard(t *testing.T) {
	cache, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	calls := 0
	transport := cache.RoundTripper(testRoundTripper(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader("fresh")), Request: request}, nil
	}))
	for _, board := range []string{"BK001", "BK002"} {
		request, _ := http.NewRequest(http.MethodGet, "https://push2.eastmoney.com/api/qt/clist/get?fs=b:"+board, nil)
		response, err := transport.RoundTrip(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("board %s status=%d", board, response.StatusCode)
		}
	}
	if calls != 2 {
		t.Fatalf("upstream called %d times for distinct boards, want 2", calls)
	}
}

func TestProvider429PersistsCooldownAcrossRequests(t *testing.T) {
    cache, err := Open(":memory:")
    if err != nil { t.Fatal(err) }
    defer cache.Close()
    calls := 0
    transport := cache.RoundTripper(testRoundTripper(func(request *http.Request) (*http.Response, error) {
        calls++
        return &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header),
            Body: io.NopCloser(strings.NewReader("limited")), Request: request}, nil
    }))
    request, _ := http.NewRequest(http.MethodGet, "https://data.10jqka.com.cn/dataapi/hotrank", nil)
    for i := 0; i < 2; i++ {
        response, err := transport.RoundTrip(request)
        if err != nil { t.Fatal(err) }
        if response.StatusCode != http.StatusTooManyRequests { t.Fatalf("status=%d", response.StatusCode) }
        if i == 1 {
            if response.Header.Get("X-Stock-Provider-Cooldown") == "" { t.Fatal("cooldown header missing") }
            retryAfter, err := strconv.Atoi(response.Header.Get("Retry-After"))
            if err != nil || retryAfter < 1700 { t.Fatalf("unexpected retry-after: %q (%v)", response.Header.Get("Retry-After"), err) }
        }
        response.Body.Close()
    }
    if calls != 1 { t.Fatalf("upstream called %d times while cooldown was active", calls) }
}
