package httpapi

import (
	"easy-stock/backend/internal/marketnetwork"
	"encoding/json"
	"net/http"
)

func (s *Server) marketNetworkSettings(w http.ResponseWriter, r *http.Request) {
	if s.marketNetwork == nil {
		writeError(w, http.StatusServiceUnavailable, "行情网络配置不可用")
		return
	}
	if r.Method == http.MethodPut {
		var settings marketnetwork.Settings
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if json.NewDecoder(r.Body).Decode(&settings) != nil {
			writeError(w, 400, "请求内容无效")
			return
		}
		if err := s.marketNetwork.Save(settings); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	}
	settings, err := s.marketNetwork.Load()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, settings)
}
