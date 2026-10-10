package hotstock

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHotStockRanksLoadsBothSourcesAndNormalizesSymbols(t *testing.T) {
	fetchedAt := "2026-10-09T08:30:00Z"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("X-Stock-Cache", "STALE")
		w.Header().Set("X-Stock-Fetched-At", fetchedAt)
		switch request.URL.Path {
		case "/ths":
			_, _ = fmt.Fprint(w, `{"status_code":0,"data":{"stock_list":[{"order":1,"code":"600519","name":"贵州茅台"},{"order":2,"code":"000001","name":"平安银行"}]}}`)
		case "/eastmoney":
			if request.Method != http.MethodPost {
				t.Fatalf("eastmoney method = %s", request.Method)
			}
			_, _ = fmt.Fprint(w, `{"status":0,"code":0,"data":[{"sc":"SH600519","rk":1},{"sc":"SZ300750","rk":2}]}`)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	client := NewClient(
		WithHTTPClient(server.Client()),
		WithSourceURLs(server.URL+"/ths", server.URL+"/eastmoney"),
	)
	lists := client.HotStockRanks(context.Background(), 100)
	if len(lists) != 2 {
		t.Fatalf("list count = %d", len(lists))
	}
	if !lists[0].Stale || lists[0].FetchedAt.UTC().Format(time.RFC3339) != fetchedAt {
		t.Fatalf("cached freshness metadata was lost: %+v", lists[0])
	}
	if lists[0].Source != "ths" || lists[0].Items[0].Symbol != "600519.SH" || lists[0].Items[1].Symbol != "000001.SZ" {
		t.Fatalf("unexpected ths list: %+v", lists[0])
	}
	if lists[1].Items[1].Symbol != "300750.SZ" || lists[1].Items[1].Rank != 2 {
		t.Fatalf("unexpected eastmoney list: %+v", lists[1])
	}
}
