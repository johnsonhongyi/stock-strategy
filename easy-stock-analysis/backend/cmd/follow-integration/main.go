package main

import (
	"easy-stock/backend/internal/httpapi"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	root := os.Args[1]
	cfg := httpapi.Config{Token: "integration-only", AutoFollowDBPath: filepath.Join(root, "account.db"), LinkageDBPath: filepath.Join(root, "linkage.db"), LinkageTargetsPath: filepath.Join(root, "targets.json"), SettingsPath: filepath.Join(root, "settings.json")}
	if len(os.Args) > 2 && os.Args[2] == "--web-auth" {
		cfg.WebAuthDBPath = filepath.Join(root, "web-users.db")
	}
	s := httpapi.NewServer(cfg)
	defer s.Close()
	if err := http.ListenAndServe("127.0.0.1:20084", s); err != nil {
		panic(err)
	}
}
