package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebLANBoundary(t *testing.T) {
	cases := []struct {
		host, peer, real, origin string
		want                     bool
	}{
		{"192.168.50.60:20073", "192.168.50.2:1234", "", "", true},
		{"10.0.0.2", "172.20.0.2:1234", "10.0.0.1", "", true},
		{"johnsonhongyi.asuscomm.com", "192.168.50.2:1234", "192.168.50.1", "", false},
		{"192.168.50.60", "8.8.8.8:1234", "192.168.50.1", "", false},
		{"192.168.50.60", "172.20.0.2:1234", "8.8.8.8", "", false},
		{"192.168.50.60", "172.20.0.2:1234", "", "https://evil.example", false},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "http://"+tc.host+"/api/auth/status", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Real-IP", tc.real)
		r.Header.Set("Origin", tc.origin)
		if got := webLAN(r); got != tc.want {
			t.Errorf("host=%s peer=%s got=%v want=%v", tc.host, tc.peer, got, tc.want)
		}
	}
}
func TestWebAuthLifecycle(t *testing.T) {
	t.Setenv("A_STOCK_BOOTSTRAP_PASSWORD", "test-bootstrap-password")
	path := filepath.Join(t.TempDir(), "users.db")
	a, err := openWebAuth(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.db.Close()
	s := &Server{webAuth: a}
	request := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://johnsonhongyi.asuscomm.com"+path, strings.NewReader(body))
		r.RemoteAddr = "172.20.0.3:1234"
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handled := s.handleWebAuth(w, r)
		if !handled {
			w.WriteHeader(204)
		}
		return w
	}
	if w := request("GET", "/api/quotes", "", nil); w.Code != 401 {
		t.Fatalf("external access %d", w.Code)
	}
	w := request("POST", "/api/auth/login", `{"username":"johnson","password":"wrong"}`, nil)
	if w.Code != 401 {
		t.Fatalf("bad login %d", w.Code)
	}
	w = request("POST", "/api/auth/login", `{"username":"johnson","password":"test-bootstrap-password"}`, nil)
	if w.Code != 200 {
		t.Fatalf("login %d %s", w.Code, w.Body)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe session cookie")
	}
	admin := cookies[0]
	if w = request("GET", "/api/quotes", "", admin); w.Code != 204 {
		t.Fatalf("session access %d", w.Code)
	}
	if w = request("POST", "/api/auth/users", `{"username":"reader","password":"readpass123","admin":false}`, admin); w.Code != 201 {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	w = request("POST", "/api/auth/login", `{"username":"reader","password":"readpass123"}`, nil)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	reader := w.Result().Cookies()[0]
	if w = request("GET", "/api/auth/users", "", reader); w.Code != 403 {
		t.Fatalf("nonadmin list %d", w.Code)
	}
	if w = request("POST", "/api/auth/password", `{"old_password":"bad","password":"updatedpass123"}`, reader); w.Code != 400 {
		t.Fatalf("wrong old password %d", w.Code)
	}
	if w = request("POST", "/api/auth/password", `{"old_password":"readpass123","password":"updatedpass123"}`, reader); w.Code != 200 {
		t.Fatalf("change %d %s", w.Code, w.Body)
	}
	if w = request("GET", "/api/quotes", "", reader); w.Code != 401 {
		t.Fatalf("old session survived %d", w.Code)
	}
	// Opening the persistent database again must not reset the bootstrap account.
	a2, err := openWebAuth(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a2.db.Close()
	var n int
	if err = a2.db.QueryRow(`SELECT count(*) FROM web_users`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("persistent users %d %v", n, err)
	}
	w = request("POST", "/api/auth/login", `{"username":"reader","password":"updatedpass123"}`, nil)
	if w.Code != 200 {
		t.Fatalf("new password %d", w.Code)
	}
	reader = w.Result().Cookies()[0]
	if w = request("POST", "/api/auth/logout", `{}`, reader); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w = request("GET", "/api/quotes", "", reader); w.Code != 401 {
		t.Fatal("logout session survived")
	}
	w = request("GET", "/api/auth/users", "", admin)
	var users []webUser
	if json.Unmarshal(w.Body.Bytes(), &users) != nil || len(users) != 2 {
		t.Fatal(w.Body)
	}

	if w = request("POST", "/api/auth/reset-password", `{"username":"johnson","password":"attacker123"}`, reader); w.Code != 401 {
		t.Fatalf("expired reset %d", w.Code)
	}
	w = request("POST", "/api/auth/login", `{"username":"reader","password":"updatedpass123"}`, nil)
	reader = w.Result().Cookies()[0]
	if w = request("POST", "/api/auth/reset-password", `{"username":"johnson","password":"attacker123"}`, reader); w.Code != 403 {
		t.Fatalf("nonadmin reset %d", w.Code)
	}
	if w = request("POST", "/api/auth/reset-password", `{"username":"reader","password":"adminreset123"}`, admin); w.Code != 200 {
		t.Fatalf("admin reset %d %s", w.Code, w.Body)
	}
	if w = request("GET", "/api/quotes", "", reader); w.Code != 401 {
		t.Fatal("reset did not revoke session")
	}
	if w = request("GET", "/api/auth/users", "", admin); w.Code != 200 {
		t.Fatal("reset revoked wrong user")
	}
	r := httptest.NewRequest("POST", "https://johnsonhongyi.asuscomm.com/api/auth/users", strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://evil.example")
	r.AddCookie(admin)
	response := httptest.NewRecorder()
	s.handleWebAuth(response, r)
	if response.Code != 403 {
		t.Fatal("cross-origin accepted")
	}
	// Shared machine tokens must not bypass public browser login.
	s.token = "desktop-secret"
	r = httptest.NewRequest("GET", "https://johnsonhongyi.asuscomm.com/api/quotes", nil)
	r.Header.Set("Authorization", "Bearer desktop-secret")
	response = httptest.NewRecorder()
	if !s.handleWebAuth(response, r) || response.Code != http.StatusUnauthorized {
		t.Fatal("shared token bypassed external login")
	}
	r = httptest.NewRequest("GET", "http://192.168.50.60:20081/api/quotes", nil)
	r.RemoteAddr = "192.168.50.2:1234"
	r.Header.Set("Authorization", "Bearer desktop-secret")
	if s.handleWebAuth(httptest.NewRecorder(), r) {
		t.Fatal("internal desktop access blocked")
	}
}
func TestWebAuthLoginThrottle(t *testing.T) {
	t.Setenv("A_STOCK_BOOTSTRAP_PASSWORD", "test-bootstrap-password")
	a, err := openWebAuth(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.db.Close()
	s := &Server{webAuth: a}
	for i := 0; i < 6; i++ {
		r := httptest.NewRequest("POST", "https://johnsonhongyi.asuscomm.com/api/auth/login", strings.NewReader(`{"username":"johnson","password":"wrong"}`))
		r.RemoteAddr = "172.20.0.3:1234"
		w := httptest.NewRecorder()
		s.handleWebAuth(w, r)
		want := 401
		if i == 5 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("attempt %d status %d", i, w.Code)
		}
	}
}

func TestWebForwardedPublicSource(t *testing.T) {
	r := httptest.NewRequest("GET", "http://192.168.50.60/api/quotes", nil)
	r.RemoteAddr = "172.20.0.3:1234"
	r.Header.Set("X-Real-IP", "192.168.50.1")
	r.Header.Set("X-Forwarded-For", "8.8.8.8, 192.168.50.1")
	if webLAN(r) {
		t.Fatal("public forwarded source bypassed login")
	}
	r.Header.Set("X-Forwarded-For", "192.168.50.2, 192.168.50.1")
	if !webLAN(r) {
		t.Fatal("private chain denied")
	}
}

func TestWebAuthServeHTTPIntegration(t *testing.T) {
	t.Setenv("A_STOCK_BOOTSTRAP_PASSWORD", "test-bootstrap-password")
	s := NewServer(Config{WebAuthDBPath: filepath.Join(t.TempDir(), "users.db"), Token: "machine-secret"})
	defer s.Close()
	if err := s.StartupError(); err != nil {
		t.Fatal(err)
	}
	s.mux.HandleFunc("/api/auth-test", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	r := httptest.NewRequest("POST", "http://johnsonhongyi.asuscomm.com:29988/api/auth/login", strings.NewReader(`{"username":"johnson","password":"test-bootstrap-password"}`))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("login via ServeHTTP %d %s", w.Code, w.Body)
	}
	cookie := w.Result().Cookies()[0]
	for _, logged := range []bool{false, true} {
		r = httptest.NewRequest("GET", "http://johnsonhongyi.asuscomm.com:29988/api/auth-test", nil)
		r.RemoteAddr = "172.20.0.3:1234"
		if logged {
			r.AddCookie(cookie)
		}
		w = httptest.NewRecorder()
		s.ServeHTTP(w, r)
		want := 401
		if logged {
			want = 204
		}
		if w.Code != want {
			t.Fatalf("logged=%v code=%d want=%d", logged, w.Code, want)
		}
	}
	r = httptest.NewRequest("GET", "http://192.168.50.60:20073/api/auth-test", nil)
	r.RemoteAddr = "192.168.50.2:1234"
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("LAN integration %d", w.Code)
	}
}

func TestWebAuthBootstrapRequiresSecretOnlyForEmptyStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	t.Setenv("A_STOCK_BOOTSTRAP_PASSWORD", "")
	if a, err := openWebAuth(path); err == nil {
		a.db.Close()
		t.Fatal("empty store accepted missing bootstrap secret")
	}
	t.Setenv("A_STOCK_BOOTSTRAP_PASSWORD", "test-bootstrap-password")
	a, err := openWebAuth(path)
	if err != nil {
		t.Fatal(err)
	}
	a.db.Close()
	t.Setenv("A_STOCK_BOOTSTRAP_PASSWORD", "")
	a, err = openWebAuth(path)
	if err != nil {
		t.Fatal("existing users must not require or reset bootstrap password:", err)
	}
	defer a.db.Close()
	var count int
	if err = a.db.QueryRow(`SELECT count(*) FROM web_users WHERE username='johnson'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("bootstrap user changed")
	}
}
