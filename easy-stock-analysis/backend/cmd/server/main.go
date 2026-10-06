package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"easy-stock/backend/internal/hermes"
	"easy-stock/backend/internal/httpapi"
	"easy-stock/backend/internal/marketcache"
	"easy-stock/backend/internal/methodology"
	"easy-stock/backend/internal/persistence"
	"easy-stock/backend/internal/runtimelog"
)

func main() {
	addr := os.Getenv("A_STOCK_ADDR")
	if addr == "" {
		addr = "127.0.0.1:20081"
	}
	settingsPath := os.Getenv("A_STOCK_SETTINGS_PATH")
	masteryCacheDir := os.Getenv("A_STOCK_MASTERY_CACHE")
	dataDir := ""
	if configDir, err := os.UserConfigDir(); err == nil {
		dataDir = preferredDataDir(configDir)
	}
	dataRoot := strings.TrimSpace(os.Getenv("A_STOCK_DATA_ROOT"))
	if dataRoot == "" {
		dataRoot = dataDir
	}
	sharedDBPath := strings.TrimSpace(os.Getenv("A_STOCK_DATA_DB"))
	if sharedDBPath == "" {
		sharedDBPath = dataPath(dataDir, "stock-data.db")
	}
	if sharedDBPath == "" {
		sharedDBPath = filepath.Join("data", "stock-data.db")
	}
	legacyDBPaths := []string{
		os.Getenv("A_STOCK_REVIEW_DB"),
		os.Getenv("A_STOCK_PORTFOLIO_DB"),
		os.Getenv("A_STOCK_RESEARCH_DB"),
		os.Getenv("A_STOCK_MARKET_EMOTION_DB"),
		os.Getenv("A_STOCK_MARKET_CACHE_DB"),
		os.Getenv("A_STOCK_THEME_RADAR_DB"),
		os.Getenv("EASY_STOCK_DATA_DB"),
		os.Getenv("EASY_STOCK_MARKET_CACHE_DB"),
	}
	if dataRoot != "" {
		for _, name := range []string{
			"service/bars.db",
			"service/market-http-cache.db",
			"easy-stock/reviews.db",
			"easy-stock/portfolio-inspections.db",
			"easy-stock/stock-research.db",
			"easy-stock/market-emotion.db",
			"easy-stock/market-provider-cache.db",
			"easy-stock/theme-radar.db",
		} {
			legacyDBPaths = append(legacyDBPaths, filepath.Join(dataRoot, filepath.FromSlash(name)))
		}
	}
	if dataDir != "" {
		for _, name := range []string{
			"reviews.db",
			"portfolio-inspections.db",
			"stock-research.db",
			"market-emotion.db",
			"market-provider-cache.db",
			"theme-radar.db",
		} {
			legacyDBPaths = append(legacyDBPaths, filepath.Join(dataDir, name))
		}
	}
	if err := persistence.MigrateSQLiteFiles(sharedDBPath, legacyDBPaths); err != nil {
		log.Fatalf("unify persistent SQLite data: %v", err)
	}
	log.Printf("persistent SQLite data unified at %s", sharedDBPath)
	if settingsPath == "" {
		settingsPath = dataPath(dataDir, "settings.json")
	}
	if masteryCacheDir == "" {
		masteryCacheDir = dataPath(dataDir, "trading-mastery")
	}
	logDirectory := os.Getenv("A_STOCK_LOG_DIR")
	if logDirectory == "" {
		logDirectory = dataPath(dataDir, "logs")
	}
	if logDirectory != "" {
		logger, closer, err := runtimelog.ConfigureStandard(logDirectory, "backend")
		if err != nil {
			log.Printf("runtime logging unavailable: %v", err)
		} else {
			defer closer.Close()
			logger.Printf("level=info event=runtime_start component=backend version=%q", runtimeVersion())
		}
	}
	marketCache, err := marketcache.Open(sharedDBPath)
	if err != nil {
		log.Fatalf("persistent market cache startup failed: %v", err)
	}
	defer func() {
		if err := marketCache.Close(); err != nil {
			log.Printf("market cache shutdown failed: %v", err)
		}
	}()
	hermesHome := os.Getenv("A_STOCK_HERMES_HOME")
	if hermesHome == "" {
		hermesHome = dataPath(dataDir, "hermes-home")
	}
	hermesWorkDir := os.Getenv("A_STOCK_HERMES_WORKDIR")
	if hermesWorkDir == "" {
		hermesWorkDir, _ = os.Getwd()
	}
	hermesGateway := hermes.NewRuntime(hermes.Config{
		RuntimeRoot: resolveHermesRuntimeRoot(),
		Home:        hermesHome,
		WorkDir:     hermesWorkDir,
		PythonPath:  os.Getenv("A_STOCK_HERMES_PYTHON"),
	})
	masteryLibrary := methodology.NewLibrary(methodology.Config{
		CacheDir:   masteryCacheDir,
		HermesHome: hermesHome,
	})
	server := httpapi.NewServer(httpapi.Config{
		Token:                os.Getenv("A_STOCK_TOKEN"),
		ReviewDBPath:         sharedDBPath,
		PortfolioDBPath:      sharedDBPath,
		StockResearchDBPath:  sharedDBPath,
		RemoteDailyReviewURL: os.Getenv("A_STOCK_DAILY_REVIEW_BASE_URL"),
		MarketEmotionDBPath:  sharedDBPath,
		MarketDataDBPath:     sharedDBPath,
		MarketCacheTransport: marketCache.RoundTripper(nil),
		ThemeRadarDBPath:     sharedDBPath,
		DuanxianxiaBaseURL:   os.Getenv("A_STOCK_DUANXIANXIA_BASE_URL"),
		WeChatAPIURL:         os.Getenv("A_STOCK_WECHAT_API_URL"),
		SettingsPath:         settingsPath,
		HermesGateway:        hermesGateway,
		MasteryLibrary:       masteryLibrary,
		Logger:               log.Default(),
		StrictPersistence:    true,
	})
	if err := server.StartupError(); err != nil {
		log.Fatalf("persistent data startup failed: %v", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go server.RunReviewScheduler(ctx)
	go server.RunRemoteDailyReviewScheduler(ctx)
	go server.RunMarketEmotionScheduler(ctx)
	go server.RunMasteryScheduler(ctx)
	httpServer := &http.Server{Addr: addr, Handler: server}
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	log.Printf("easy-stock data foundation listening on http://%s", addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	if err := server.Close(); err != nil {
		log.Printf("close persistent data: %v", err)
	}
}

func runtimeVersion() string {
	if value := strings.TrimSpace(os.Getenv("A_STOCK_APP_VERSION")); value != "" {
		return value
	}
	return "development"
}

func preferredDataDir(configDir string) string {
	current := filepath.Join(configDir, "easy-stock")
	if isFile(filepath.Join(current, "settings.json")) {
		return current
	}
	legacy := filepath.Join(configDir, "a-stock-ai")
	if isFile(filepath.Join(legacy, "settings.json")) {
		return legacy
	}
	return current
}

func isFile(filePath string) bool {
	info, err := os.Stat(filePath)
	return err == nil && !info.IsDir()
}

func dataPath(dataDir, name string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, name)
}

func resolveHermesRuntimeRoot() string {
	if configured := os.Getenv("A_STOCK_HERMES_RUNTIME_ROOT"); configured != "" {
		return configured
	}
	candidates := []string{}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Clean(filepath.Join(filepath.Dir(executable), "..", "hermes-runtime")))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(cwd, "desktop", "resources", "hermes-runtime"),
			filepath.Join(cwd, "..", "desktop", "resources", "hermes-runtime"),
		)
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}
