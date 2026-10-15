package main

import (
	"easy-stock/backend/internal/httpapi"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	root := os.Args[1]
	s := httpapi.NewServer(httpapi.Config{Token: "integration-only", AutoFollowDBPath: filepath.Join(root, "account.db"), LinkageDBPath: filepath.Join(root, "linkage.db"), LinkageTargetsPath: filepath.Join(root, "targets.json"), SettingsPath: filepath.Join(root, "settings.json")})
	defer s.Close()
	if err := http.ListenAndServe("127.0.0.1:20084", s); err != nil {
		panic(err)
	}
}
