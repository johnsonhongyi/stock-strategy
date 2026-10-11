package httpapi

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

const maxLinkagePeerBody = 640 << 10

func (s *Server) withCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-A-Stock-Token, X-Request-ID, X-Stock-Cache-Refresh, X-Linkage-Node-ID, X-Linkage-Workspace-ID, X-Linkage-Timestamp, X-Linkage-Nonce, X-Linkage-Signature")
	w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
}

func (s *Server) authorized(r *http.Request) bool {
	if s.token == "" || r.URL.Path == "/api/health" || r.Method == http.MethodOptions {
		return true
	}
	if r.URL.Query().Get("token") == s.token {
		return true
	}
	if r.Header.Get("X-A-Stock-Token") == s.token {
		return true
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(auth, "Bearer ") && strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")) == s.token {
		return true
	}
	return false
}

func (s *Server) authorizedLinkage(r *http.Request) bool {
	if s == nil {
		return false
	}
	if token := strings.TrimSpace(s.token); token != "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(auth, "Bearer ") && strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")) == token {
			return true
		}
		if r.Header.Get("X-A-Stock-Token") == token {
			return true
		}
	}
	if s.linkage == nil {
		return false
	}
	var body []byte
	if r.Body != nil {
		var err error
		body, err = io.ReadAll(io.LimitReader(r.Body, maxLinkagePeerBody+1))
		_ = r.Body.Close()
		if err != nil || len(body) > maxLinkagePeerBody {
			return false
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	return s.linkage.AuthenticatePeerRequest(r.Method, r.URL.RequestURI(), r.Header, body)
}

func (s *Server) authorizedLinkageAdmin(r *http.Request) bool {
	if s == nil || strings.TrimSpace(s.token) == "" {
		return false
	}
	token := strings.TrimSpace(s.token)
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	return strings.HasPrefix(auth, "Bearer ") && strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")) == token ||
		r.Header.Get("X-A-Stock-Token") == token
}
