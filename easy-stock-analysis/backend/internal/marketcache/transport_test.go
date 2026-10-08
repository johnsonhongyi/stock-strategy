package marketcache

import (
	"context"
	"errors"
	"io"
	"net/http"
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
