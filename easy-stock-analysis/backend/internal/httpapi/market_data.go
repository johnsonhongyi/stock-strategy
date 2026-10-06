package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"easy-stock/backend/internal/marketbars"
)

var managedUSSymbol = regexp.MustCompile(`^[A-Z][A-Z0-9.]{0,9}$`)
var managedCryptoSymbol = regexp.MustCompile(`^[A-Z0-9-]{2,12}$`)

type managedStockRequest struct {
	Market string `json:"market"`
	Symbol string `json:"symbol"`
	Name   string `json:"name"`
}

func normalizeManagedStock(market, symbol string) (string, string, error) {
	code, normalizedMarket := marketbars.NormalizeSymbol(symbol, market)
	switch normalizedMarket {
	case "CN":
		if !regexp.MustCompile(`^\d{6}$`).MatchString(code) {
			return "", "", fmt.Errorf("A股代码须为6位数字")
		}
	case "US":
		if !managedUSSymbol.MatchString(code) {
			return "", "", fmt.Errorf("美股代码格式无效")
		}
	case "CRYPTO":
		if !managedCryptoSymbol.MatchString(code) {
			return "", "", fmt.Errorf("币种代码格式无效")
		}
	default:
		return "", "", fmt.Errorf("市场须为 CN、US 或 CRYPTO")
	}
	return code, normalizedMarket, nil
}

func (s *Server) marketDataStocks(w http.ResponseWriter, r *http.Request) {
	if s.marketBars == nil {
		writeError(w, http.StatusServiceUnavailable, "本地行情数据库不可用")
		return
	}
	market := strings.TrimSpace(r.URL.Query().Get("market"))
	items, err := s.marketBars.ListStocks(r.Context(), market)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items})
}

func (s *Server) marketDataStockAdd(w http.ResponseWriter, r *http.Request) {
	if s.marketBars == nil {
		writeError(w, http.StatusServiceUnavailable, "本地行情数据库不可用")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var request managedStockRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "请求内容无效")
		return
	}
	code, market, err := normalizeManagedStock(request.Market, request.Symbol)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimSpace(request.Name)
	if len([]rune(name)) > 80 {
		writeError(w, http.StatusBadRequest, "股票名称不能超过80个字符")
		return
	}
	if err := s.marketBars.SetTrackedStock(r.Context(), market, code, name, true, "manual"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"market": market, "symbol": code, "tracked": true})
}

func (s *Server) marketDataStockRemove(w http.ResponseWriter, r *http.Request) {
	if s.marketBars == nil {
		writeError(w, http.StatusServiceUnavailable, "本地行情数据库不可用")
		return
	}
	code, market, err := normalizeManagedStock(r.PathValue("market"), r.PathValue("symbol"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.marketBars.SetTrackedStock(r.Context(), market, code, "", false, "removed"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"market": market, "symbol": code, "tracked": false})
}

func (s *Server) marketDataStockBars(w http.ResponseWriter, r *http.Request) {
	if s.marketBars == nil {
		writeError(w, http.StatusServiceUnavailable, "本地行情数据库不可用")
		return
	}
	code, market, err := normalizeManagedStock(r.PathValue("market"), r.PathValue("symbol"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit := 120
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 2000 {
			writeError(w, http.StatusBadRequest, "limit须在1到2000之间")
			return
		}
	}
	lines, _, err := s.marketBars.ReadDaily(r.Context(), code, market, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"market": market, "symbol": code, "data": lines})
}

func (s *Server) marketDataStockCacheClear(w http.ResponseWriter, r *http.Request) {
	if s.marketBars == nil {
		writeError(w, http.StatusServiceUnavailable, "本地行情数据库不可用")
		return
	}
	code, market, err := normalizeManagedStock(r.PathValue("market"), r.PathValue("symbol"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	deleted, err := s.marketBars.ClearDaily(r.Context(), market, code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"market": market, "symbol": code, "deleted_bars": deleted})
}
