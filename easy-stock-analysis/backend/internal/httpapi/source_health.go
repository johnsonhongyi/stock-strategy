package httpapi

import (
	"context"
	"easy-stock/backend/internal/foundation"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type sourceHealthMonitor struct {
	next     http.RoundTripper
	mu       sync.RWMutex
	observed map[string]foundation.SourceHealth
}

func newSourceHealthMonitor(next http.RoundTripper) *sourceHealthMonitor {
	if next == nil {
		next = http.DefaultTransport
	}
	return &sourceHealthMonitor{next: next, observed: make(map[string]foundation.SourceHealth)}
}

func observedSource(host string) string {
	host = strings.ToLower(host)
	domains := map[string]string{"eastmoney.com": "eastmoney", "emdatah5.com": "eastmoney", "sina.com.cn": "sina", "sinajs.cn": "sina", "qq.com": "tencent", "gtimg.cn": "tencent", "cls.cn": "cls", "duanxianxia.com": "duanxianxia", "kaipanla.com": "duanxianxia", "tradingview.com": "tradingview"}
	for domain, source := range domains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return source
		}
	}
	return ""
}

func (m *sourceHealthMonitor) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := m.next.RoundTrip(request)
	id := observedSource(request.URL.Hostname())
	if id == "" || errors.Is(err, context.Canceled) {
		return response, err
	}
	state := foundation.SourceHealth{ID: id, Status: "error", Message: "最近访问失败", CheckedAt: time.Now()}
	if err == nil && response != nil {
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			state.OK, state.Status, state.Message = true, "ok", "最近访问成功"
			switch strings.ToUpper(response.Header.Get("X-Stock-Cache")) {
			case "HIT":
				state.Status, state.Message = "cached", "最近读取缓存成功；未重新探测上游"
			case "STALE":
				state.OK, state.Status, state.Message = false, "stale", "上游暂不可用，正在使用旧缓存"
			}
		} else {
			state.Message = fmt.Sprintf("最近访问返回 HTTP %d", response.StatusCode)
		}
	}
	m.mu.Lock()
	m.observed[id] = state
	m.mu.Unlock()
	return response, err
}

func (m *sourceHealthMonitor) snapshot(now time.Time) []foundation.SourceHealth {
	sources := []foundation.SourceHealth{
		{ID: "duanxianxia", Name: "短线侠 / 开盘啦", Category: "theme,leaders,limit-up,concept"},
		{ID: "eastmoney", Name: "东方财富", Category: "quote,kline,f10,report"},
		{ID: "sina", Name: "新浪财经", Category: "quote,kline,money-flow"},
		{ID: "tencent", Name: "腾讯财经", Category: "quote,index,hk"},
		{ID: "cls", Name: "财联社", Category: "news,calendar"},
		{ID: "tradingview", Name: "TradingView", Category: "news"},
		{ID: "tushare", Name: "Tushare", Category: "basic,daily,index"},
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range sources {
		item := &sources[i]
		state, ok := m.observed[item.ID]
		if !ok {
			item.Status, item.Message = "unknown", "尚无实际访问记录"
			continue
		}
		item.OK, item.Status, item.Message, item.CheckedAt = state.OK, state.Status, state.Message, state.CheckedAt
		if now.Sub(state.CheckedAt) > 5*time.Minute {
			item.OK, item.Status, item.Message = false, "unknown", "最近5分钟没有新的访问记录"
		}
	}
	return sources
}
