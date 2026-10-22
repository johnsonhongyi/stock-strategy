package httpapi

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	_ "modernc.org/sqlite"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type webAuthStore struct {
	db       *sql.DB
	mu       sync.Mutex
	failures map[string]loginFailures
}
type loginFailures struct {
	count int
	until time.Time
}
type webUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Admin    bool   `json:"admin"`
}

const webCookie = "stock_session"

func openWebAuth(path string) (*webAuthStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(e error) (*webAuthStore, error) { db.Close(); return nil, e }
	_, err = db.Exec(`PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS web_users(id INTEGER PRIMARY KEY,username TEXT NOT NULL UNIQUE,salt TEXT NOT NULL,password TEXT NOT NULL,admin INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS web_sessions(token TEXT PRIMARY KEY,user_id INTEGER NOT NULL,expires INTEGER NOT NULL);`)
	if err != nil {
		return fail(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		return fail(err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM web_users`).Scan(&count); err != nil {
		return fail(err)
	}
	if count == 0 {
		password := os.Getenv("A_STOCK_BOOTSTRAP_PASSWORD")
		if len(password) < 8 || len(password) > 256 {
			return fail(fmt.Errorf("A_STOCK_BOOTSTRAP_PASSWORD must be 8-256 bytes for an empty user database"))
		}
		salt := randomHex()
		hash, hashErr := passwordHash(password, salt)
		if hashErr != nil {
			return fail(hashErr)
		}
		_, err = db.Exec(`INSERT INTO web_users(username,salt,password,admin) SELECT 'johnson',?,?,1 WHERE NOT EXISTS(SELECT 1 FROM web_users)`, salt, hash)
		if err != nil {
			return fail(err)
		}
	}
	return &webAuthStore{db: db, failures: map[string]loginFailures{}}, nil
}
func passwordHash(password, salt string) (string, error) {
	raw, err := hex.DecodeString(salt)
	if err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, raw, 600000, 32)
	return hex.EncodeToString(key), err
}
func randomHex() string { return hex.EncodeToString([]byte(rand.Text())) }

func sessionHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
func (a *webAuthStore) clearUserFailures(username string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	suffix := "|" + strings.ToLower(strings.TrimSpace(username))
	for k := range a.failures {
		if strings.HasSuffix(k, suffix) {
			delete(a.failures, k)
		}
	}
}

func (a *webAuthStore) user(r *http.Request) (webUser, bool) {
	var u webUser
	c, err := r.Cookie(webCookie)
	if err != nil {
		return u, false
	}
	err = a.db.QueryRow(`SELECT u.id,u.username,u.admin FROM web_users u JOIN web_sessions s ON s.user_id=u.id WHERE s.token=? AND s.expires>?`, sessionHash(c.Value), time.Now().Unix()).Scan(&u.ID, &u.Username, &u.Admin)
	return u, err == nil
}

// Only literal private destination addresses qualify. A public domain stays protected
// even behind NAT; untrusted forwarded headers can never grant LAN access.
func webLAN(r *http.Request) bool {
	host, err := url.Parse("//" + r.Host)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host.Hostname())
	if ip == nil || !(ip.IsPrivate() || ip.IsLoopback()) {
		return false
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip = net.ParseIP(peer)
	if ip == nil || !(ip.IsPrivate() || ip.IsLoopback()) {
		return false
	}
	if raw := r.Header.Get("X-Real-IP"); raw != "" {
		ip = net.ParseIP(raw)
		if ip == nil || !(ip.IsPrivate() || ip.IsLoopback()) {
			return false
		}
	}
	for _, raw := range r.Header.Values("X-Forwarded-For") {
		for _, hop := range strings.Split(raw, ",") {
			ip := net.ParseIP(strings.TrimSpace(hop))
			if ip == nil || !(ip.IsPrivate() || ip.IsLoopback()) {
				return false
			}
		}
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
		if e != nil || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	}
	return true
}
func webSameOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, e := url.Parse(o)
		return e == nil && (u.Scheme == "http" || u.Scheme == "https") && strings.EqualFold(u.Host, r.Host)
	}
	return true
}
func (s *Server) handleWebAuth(w http.ResponseWriter, r *http.Request) bool {
	a := s.webAuth
	path := r.URL.Path
	u, logged := a.user(r)
	if !strings.HasPrefix(path, "/api/auth/") {
		if path == "/api/health" || r.Method == http.MethodOptions {
			return false
		}
		// Public browser access always requires a session. Legacy shared tokens
		// cannot bypass login; desktop HMAC peers retain their separate protocol.
		machine := false
		if strings.HasPrefix(path, "/api/v1/linkage/") {
			for key := range r.Header {
				if strings.HasPrefix(strings.ToLower(key), "x-linkage-") {
					machine = s.authorizedLinkage(r)
					break
				}
			}
		}
		if !logged && !webLAN(r) && !machine {
			writeError(w, 401, "login required")
			return true
		}
		if logged && r.Method != http.MethodGet && r.Method != http.MethodHead && !webSameOrigin(r) {
			writeError(w, 403, "invalid origin")
			return true
		}
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	if path == "/api/auth/status" && r.Method == http.MethodGet {
		writeJSON(w, 200, map[string]any{"authenticated": logged, "lan": webLAN(r), "user": u})
		return true
	}
	isUsersEndpoint := path == "/api/auth/users"
	validMethod := r.Method == http.MethodPost ||
		(isUsersEndpoint && (r.Method == http.MethodGet || r.Method == http.MethodPut || r.Method == http.MethodDelete))
	if !validMethod {
		writeError(w, 405, "method not allowed")
		return true
	}
	if !webSameOrigin(r) {
		writeError(w, 403, "invalid origin")
		return true
	}
	var input struct {
		ID          int64  `json:"id"`
		Username    string `json:"username"`
		Password    string `json:"password"`
		OldPassword string `json:"old_password"`
		Admin       *bool  `json:"admin"`
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPut || (r.Method == http.MethodDelete && r.Body != nil && r.ContentLength > 0) {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err.Error() != "EOF" {
			writeError(w, 400, "invalid request")
			return true
		}
	}
	if r.Method == http.MethodDelete && input.ID == 0 && input.Username == "" {
		if qID := r.URL.Query().Get("id"); qID != "" {
			fmt.Sscanf(qID, "%d", &input.ID)
		}
		if qUser := r.URL.Query().Get("username"); qUser != "" {
			input.Username = qUser
		}
	}
	setCookie := func(value string, age int) {
		http.SetCookie(w, &http.Cookie{Name: webCookie, Value: value, Path: "/", HttpOnly: true, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", SameSite: http.SameSiteStrictMode, MaxAge: age})
	}
	switch path {
	case "/api/auth/login":
		peer, _, _ := net.SplitHostPort(r.RemoteAddr)
		key := peer + "|" + strings.ToLower(strings.TrimSpace(input.Username))
		a.mu.Lock()
		defer a.mu.Unlock()
		f := a.failures[key]
		if f.count >= 5 && time.Now().Before(f.until) {
			writeError(w, 429, "登录失败过多，请15分钟后重试")
			return true
		}
		var salt, stored string
		err := a.db.QueryRow(`SELECT id,username,admin,salt,password FROM web_users WHERE LOWER(username)=LOWER(?)`, strings.TrimSpace(input.Username)).Scan(&u.ID, &u.Username, &u.Admin, &salt, &stored)
		if err != nil {
			salt = "00000000000000000000000000000000"
		}
		hash, hashErr := passwordHash(input.Password, salt)
		if err != nil || hashErr != nil || subtle.ConstantTimeCompare([]byte(hash), []byte(stored)) != 1 {
			if time.Now().After(f.until) {
				f.count = 0
			}
			f.count++
			f.until = time.Now().Add(15 * time.Minute)
			if len(a.failures) > 4096 {
				for k, v := range a.failures {
					if time.Now().After(v.until) {
						delete(a.failures, k)
					}
				}
				if len(a.failures) > 4096 {
					writeError(w, 429, "请稍后重试")
					return true
				}
			}
			a.failures[key] = f
			writeError(w, 401, "用户名或密码错误")
			return true
		}
		delete(a.failures, key)
		token := randomHex()
		if _, err = a.db.Exec(`DELETE FROM web_sessions WHERE expires<=?`, time.Now().Unix()); err == nil {
			var result sql.Result
			result, err = a.db.Exec(`INSERT INTO web_sessions(token,user_id,expires) SELECT ?,id,? FROM web_users WHERE id=? AND password=?`, sessionHash(token), time.Now().Add(12*time.Hour).Unix(), u.ID, stored)
			if err == nil {
				n, e := result.RowsAffected()
				if e != nil || n != 1 {
					err = fmt.Errorf("credentials changed concurrently")
				}
			}
		}
		if err != nil {
			writeError(w, 500, "会话保存失败")
			return true
		}
		setCookie(token, 43200)
		writeJSON(w, 200, u)
	case "/api/auth/logout":
		if c, e := r.Cookie(webCookie); e == nil {
			if _, e = a.db.Exec(`DELETE FROM web_sessions WHERE token=?`, sessionHash(c.Value)); e != nil {
				writeError(w, 500, "退出失败")
				return true
			}
		}
		setCookie("", -1)
		writeJSON(w, 200, map[string]bool{"ok": true})
	case "/api/auth/users":
		if !logged || !u.Admin {
			writeError(w, 403, "需要管理员登录")
			return true
		}
		if r.Method == http.MethodGet {
			rows, err := a.db.Query(`SELECT id,username,admin FROM web_users ORDER BY id`)
			if err != nil {
				writeError(w, 500, "读取失败")
				return true
			}
			defer rows.Close()
			users := []webUser{}
			for rows.Next() {
				var x webUser
				if err = rows.Scan(&x.ID, &x.Username, &x.Admin); err != nil {
					break
				}
				users = append(users, x)
			}
			if err != nil || rows.Err() != nil {
				writeError(w, 500, "读取失败")
				return true
			}
			writeJSON(w, 200, users)
			return true
		}
		if r.Method == http.MethodDelete {
			targetID := input.ID
			var targetUsername string
			var targetAdmin bool
			var err error
			if targetID > 0 {
				err = a.db.QueryRow(`SELECT id,username,admin FROM web_users WHERE id=?`, targetID).Scan(&targetID, &targetUsername, &targetAdmin)
			} else if strings.TrimSpace(input.Username) != "" {
				err = a.db.QueryRow(`SELECT id,username,admin FROM web_users WHERE LOWER(username)=LOWER(?)`, strings.TrimSpace(input.Username)).Scan(&targetID, &targetUsername, &targetAdmin)
			} else {
				writeError(w, 400, "缺少待删除用户标识")
				return true
			}
			if err != nil {
				writeError(w, 404, "用户不存在")
				return true
			}
			if strings.EqualFold(targetUsername, "johnson") || targetID == 1 {
				writeError(w, 400, "系统默认主管理员不可删除")
				return true
			}
			if targetID == u.ID {
				writeError(w, 400, "不可删除当前登录账号")
				return true
			}
			tx, err := a.db.Begin()
			if err != nil {
				writeError(w, 500, "删除失败")
				return true
			}
			defer tx.Rollback()
			if _, err = tx.Exec(`DELETE FROM web_sessions WHERE user_id=?`, targetID); err != nil {
				writeError(w, 500, "会话清理失败")
				return true
			}
			if _, err = tx.Exec(`DELETE FROM web_users WHERE id=?`, targetID); err != nil {
				writeError(w, 500, "用户删除失败")
				return true
			}
			if err = tx.Commit(); err != nil {
				writeError(w, 500, "删除提交失败")
				return true
			}
			a.clearUserFailures(targetUsername)
			writeJSON(w, 200, map[string]bool{"ok": true})
			return true
		}
		if r.Method == http.MethodPut {
			targetID := input.ID
			var targetUsername string
			var currentAdmin bool
			var err error
			if targetID > 0 {
				err = a.db.QueryRow(`SELECT id,username,admin FROM web_users WHERE id=?`, targetID).Scan(&targetID, &targetUsername, &currentAdmin)
			} else if strings.TrimSpace(input.Username) != "" {
				err = a.db.QueryRow(`SELECT id,username,admin FROM web_users WHERE LOWER(username)=LOWER(?)`, strings.TrimSpace(input.Username)).Scan(&targetID, &targetUsername, &currentAdmin)
			} else {
				writeError(w, 400, "缺少待修改用户标识")
				return true
			}
			if err != nil {
				writeError(w, 404, "用户不存在")
				return true
			}
			if input.Admin != nil {
				newAdmin := *input.Admin
				if (strings.EqualFold(targetUsername, "johnson") || targetID == 1) && !newAdmin {
					writeError(w, 400, "系统默认主管理员权限不可取消")
					return true
				}
				if targetID == u.ID && !newAdmin {
					writeError(w, 400, "不可取消当前登录管理员自身权限")
					return true
				}
				adminInt := 0
				if newAdmin {
					adminInt = 1
				}
				if _, err = a.db.Exec(`UPDATE web_users SET admin=? WHERE id=?`, adminInt, targetID); err != nil {
					writeError(w, 500, "角色更新失败")
					return true
				}
			}
			writeJSON(w, 200, map[string]bool{"ok": true})
			return true
		}
		input.Username = strings.TrimSpace(input.Username)
		if len(input.Username) < 1 || len(input.Username) > 64 || len(input.Password) < 8 || len(input.Password) > 256 {
			writeError(w, 400, "用户名1–64字符，密码8–256字节")
			return true
		}
		var existingCount int
		_ = a.db.QueryRow(`SELECT COUNT(*) FROM web_users WHERE LOWER(username)=LOWER(?)`, input.Username).Scan(&existingCount)
		if existingCount > 0 {
			writeError(w, 400, "添加失败，用户名可能已存在")
			return true
		}
		adminInt := 0
		if input.Admin != nil && *input.Admin {
			adminInt = 1
		}
		salt := randomHex()
		hash, err := passwordHash(input.Password, salt)
		if err == nil {
			_, err = a.db.Exec(`INSERT INTO web_users(username,salt,password,admin) VALUES(?,?,?,?)`, input.Username, salt, hash, adminInt)
		}
		if err != nil {
			writeError(w, 400, "添加失败，用户名可能已存在")
			return true
		}
		a.clearUserFailures(input.Username)
		writeJSON(w, 201, map[string]bool{"ok": true})
	case "/api/auth/password", "/api/auth/reset-password":
		if !logged {
			writeError(w, 401, "需要登录")
			return true
		}
		if len(input.Password) < 8 || len(input.Password) > 256 {
			writeError(w, 400, "密码需8–256字节")
			return true
		}
		reset := path == "/api/auth/reset-password"
		targetID := u.ID
		targetUsername := u.Username
		if reset {
			if !u.Admin {
				writeError(w, 403, "需要管理员登录")
				return true
			}
			var foundID int64
			var foundUsername string
			var queryErr error
			if input.ID > 0 {
				queryErr = a.db.QueryRow(`SELECT id, username FROM web_users WHERE id=?`, input.ID).Scan(&foundID, &foundUsername)
			} else {
				queryErr = a.db.QueryRow(`SELECT id, username FROM web_users WHERE LOWER(username)=LOWER(?)`, strings.TrimSpace(input.Username)).Scan(&foundID, &foundUsername)
			}
			if queryErr != nil {
				writeError(w, 404, "用户不存在")
				return true
			}
			targetID = foundID
			targetUsername = foundUsername
		}
		var salt, stored string
		err := a.db.QueryRow(`SELECT salt,password FROM web_users WHERE id=?`, targetID).Scan(&salt, &stored)
		if err != nil {
			writeError(w, 404, "用户不存在")
			return true
		}
		if !reset {
			hash, e := passwordHash(input.OldPassword, salt)
			if e != nil || subtle.ConstantTimeCompare([]byte(hash), []byte(stored)) != 1 {
				writeError(w, 400, "原密码错误")
				return true
			}
		}
		salt = randomHex()
		hash, err := passwordHash(input.Password, salt)
		if err != nil {
			writeError(w, 500, "修改失败")
			return true
		}
		tx, err := a.db.Begin()
		if err != nil {
			writeError(w, 500, "修改失败")
			return true
		}
		defer tx.Rollback()
		result, err := tx.Exec(`UPDATE web_users SET salt=?,password=? WHERE id=? AND password=?`, salt, hash, targetID, stored)
		if err == nil {
			n, e := result.RowsAffected()
			if e != nil || n != 1 {
				err = fmt.Errorf("password changed concurrently")
			}
		}
		if err == nil {
			_, err = tx.Exec(`DELETE FROM web_sessions WHERE user_id=?`, targetID)
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			writeError(w, 500, "修改失败，请重试")
			return true
		}
		a.clearUserFailures(targetUsername)
		if caller, ok := a.user(r); !ok || caller.ID == targetID {
			setCookie("", -1)
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	default:
		writeError(w, 404, "not found")
	}
	return true
}
