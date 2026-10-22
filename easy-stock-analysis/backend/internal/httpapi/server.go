package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"easy-stock/backend/internal/appsettings"
	"easy-stock/backend/internal/foundation"
	"easy-stock/backend/internal/hermes"
	"easy-stock/backend/internal/linkage"
	"easy-stock/backend/internal/marketbars"
	"easy-stock/backend/internal/marketcache"
	"easy-stock/backend/internal/marketemotion"
	"easy-stock/backend/internal/marketnetwork"
	"easy-stock/backend/internal/methodology"
	"easy-stock/backend/internal/portfolioinspection"
	"easy-stock/backend/internal/providers/cls"
	"easy-stock/backend/internal/providers/duanxianxia"
	"easy-stock/backend/internal/providers/eastmoney"
	futurespositionprovider "easy-stock/backend/internal/providers/futuresposition"
	"easy-stock/backend/internal/providers/hotstock"
	marketoverviewprovider "easy-stock/backend/internal/providers/marketoverview"
	"easy-stock/backend/internal/providers/sina"
	"easy-stock/backend/internal/providers/tdx"
	"easy-stock/backend/internal/providers/tencent"
	"easy-stock/backend/internal/review"
	"easy-stock/backend/internal/runtimelog"
	"easy-stock/backend/internal/sector"
	"easy-stock/backend/internal/stockanalysis"
	"easy-stock/backend/internal/strategy/inflection"
)

type Server struct {
	desktopDBMu           sync.Mutex
	desktopChatDB         *sql.DB
	desktopPrunedAt       time.Time
	desktopSignals        desktopSignals
	autoFollowDBPath      string
	webAuth               *webAuthStore
	linkageContextMu      sync.Mutex
	marketStatusCache     systemStatusCache
	tdx                   *tdx.Client
	marketNetwork         *marketnetwork.Config
	sourceHealth          *sourceHealthMonitor
	ladderThemeAI         *ladderThemeAI
	mux                   *http.ServeMux
	token                 string
	linkage               *linkage.Service
	realtimeProvider      RealtimeProvider
	kLinePrimary          KLineProvider
	kLineFallback         KLineProvider
	newsProvider          NewsProvider
	sectorMap             SectorMapProvider
	themeOverview         ThemeOverviewProvider
	limitUpProvider       LimitUpProvider
	marketPools           MarketPoolProvider
	stockConcepts         StockConceptProvider
	stockBusiness         StockBusinessProfileProvider
	stockDirectory        StockDirectoryProvider
	hotStockProvider      HotStockProvider
	futuresPosition       FuturesPositionProvider
	marketOverview        MarketOverviewProvider
	inflection            InflectionEvaluator
	themeSnapshots        *themeSnapshotCache
	limitUpSnapshots      *limitUpLadderCache
	limitUpProgress       *shortTermCache[limitUpLadderData]
	emotionProgress       *shortTermCache[marketemotion.History]
	stockDirectories      *stockDirectoryCache
	hotStockRanks         *hotStockRankCache
	marketSnapshots       *marketOverviewCache
	marketEmotion         *marketEmotionEngine
	marketEmotionIntraday *marketEmotionIntradayCache
	reviewStore           *review.Store
	portfolioStore        *portfolioinspection.Store
	portfolioInspection   *portfolioinspection.Service
	portfolioExpectation  *portfolioinspection.ExpectationService
	stockResearchStore    *stockanalysis.ResearchStore
	stockResearch         *stockanalysis.ResearchService
	reviewImporter        ReviewImporter
	wechatAPIURL          string
	settingsStore         *appsettings.Store
	reviewAutomation      *review.Automation
	remoteDailySync       *review.RemoteDailySync
	hermesGateway         hermes.Gateway
	usageGateway          hermes.Gateway
	masteryLibrary        *methodology.Library
	marketEmotionStore    *marketemotion.Store
	marketBars            *marketbars.Store
	liveMarketRuntime     *liveMarketRuntimeState
	startedAt             time.Time
	themeRadarStore       *duanxianxia.Store
	themeProgress         *themeProgressCache
	startupError          error
	logger                *log.Logger
	tokenUsage            *tokenUsageStore
}

func newMarketHTTPClient(timeout time.Duration, transport http.RoundTripper) *http.Client {
	return &http.Client{Timeout: timeout, Transport: transport}
}

func NewServer(config any) *Server {
	cfg := normalizeConfig(config)
	if cfg.AutoFollowDBPath == "" {
		cfg.AutoFollowDBPath = os.Getenv("EASY_STOCK_FOLLOW_DB")
		if cfg.AutoFollowDBPath == "" {
			cfg.AutoFollowDBPath = "/data/auto-follow/account.db"
		}
	}
	tokenUsage := newTokenUsageStore(cfg.SettingsPath)
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	var startupErrors []error
	sourceHealth := newSourceHealthMonitor(cfg.MarketCacheTransport)
	cfg.MarketCacheTransport = sourceHealth
	sinaClient := sina.NewClient(sina.WithHTTPClient(newMarketHTTPClient(10*time.Second, cfg.MarketCacheTransport)))
	eastMoneyClient := eastmoney.NewClient(eastmoney.WithHTTPClient(newMarketHTTPClient(15*time.Second, cfg.MarketCacheTransport)))
	tencentClient := tencent.NewClient(tencent.WithHTTPClient(newMarketHTTPClient(12*time.Second, cfg.MarketCacheTransport)))
	clsClient := cls.NewClient(cls.WithHTTPClient(newMarketHTTPClient(12*time.Second, cfg.MarketCacheTransport)))
	if cfg.Realtime == nil {
		cfg.Realtime = sinaClient
	}
	usingDefaultKLine := cfg.KLinePrimary == nil
	var international KLineProvider
	if cfg.KLinePrimary == nil {
		international = &internationalKLineProvider{client: newMarketHTTPClient(20*time.Second, cfg.MarketCacheTransport)}
		cfg.KLinePrimary = eastMoneyClient
	}
	if cfg.KLineFallback == nil {
		cfg.KLineFallback = sinaClient
	}
	if cfg.News == nil {
		cfg.News = clsClient
	}
	var kaipanlaService *duanxianxia.Service
	if strings.TrimSpace(cfg.ThemeRadarDBPath) != "" {
		if store, err := duanxianxia.OpenStore(cfg.ThemeRadarDBPath); err == nil {
			client := duanxianxia.NewClient(duanxianxia.ClientConfig{
				BaseURL:    cfg.DuanxianxiaBaseURL,
				HTTPClient: newMarketHTTPClient(12*time.Second, cfg.MarketCacheTransport),
			})
			kaipanlaService = duanxianxia.NewService(client, store, duanxianxia.ServiceConfig{
				RefreshInterval:  5 * time.Minute,
				LeaderThemeLimit: 3,
			})
		} else if cfg.StrictPersistence {
			startupErrors = append(startupErrors, fmt.Errorf("open theme radar database: %w", err))
		}
	}
	usingDefaultLimitUp := cfg.LimitUp == nil
	if usingDefaultLimitUp {
		if kaipanlaService != nil {
			cfg.LimitUp = duanxianxia.NewLimitUpProvider(kaipanlaService, eastMoneyClient)
		} else {
			cfg.LimitUp = eastMoneyClient
		}
	}
	if cfg.MarketPools == nil {
		cfg.MarketPools = eastMoneyClient
	}
	if cfg.StockConcept == nil && usingDefaultLimitUp {
		cfg.StockConcept = eastMoneyClient
	}
	if cfg.StockBusiness == nil {
		cfg.StockBusiness = eastMoneyClient
	}
	if cfg.StockDirectory == nil {
		cfg.StockDirectory = eastMoneyClient
	}
	if cfg.MarketOverview == nil {
		cfg.MarketOverview = marketoverviewprovider.New(eastMoneyClient, tencentClient, tencentClient, sinaClient)
	}
	if cfg.SectorMap == nil {
		mapper := sector.NewMapper(
			eastMoneyClient,
			sector.WithQuoteProvider(sinaClient),
			sector.WithLimitUpProvider(cfg.LimitUp),
			sector.WithStockCatalogProvider(eastMoneyClient),
		)
		var defaultSectorMap SectorMapProvider = mapper
		var defaultThemeOverview ThemeOverviewProvider = mapper
		var radarFallback sector.RadarFallback = mapper
		if cfg.ThemeRadarFallback != nil {
			radarFallback = cfg.ThemeRadarFallback
		}
		var radarSource sector.RadarSnapshotSource
		if kaipanlaService != nil {
			radarSource = kaipanlaService
		}
		radar := sector.NewRadarProvider(radarSource, radarFallback, cfg.Realtime, sector.RadarProviderConfig{
			IndustryMomentum:  cfg.MarketOverview,
			IndustryStocks:    tencentClient,
			FallbackFillLimit: 16,
		})
		defaultSectorMap = radar
		defaultThemeOverview = radar
		cfg.SectorMap = defaultSectorMap
		if cfg.ThemeOverview == nil {
			cfg.ThemeOverview = defaultThemeOverview
		}
	}
	if cfg.Inflection == nil {
		cfg.Inflection = inflection.NewEngine(inflection.DefaultConfig())
	}
	if cfg.MarketDataStore == nil && strings.TrimSpace(cfg.MarketDataDBPath) != "" {
		store, err := marketbars.Open(cfg.MarketDataDBPath)
		if err == nil {
			cfg.MarketDataStore = store
		} else if cfg.StrictPersistence {
			startupErrors = append(startupErrors, fmt.Errorf("open shared market data database: %w", err))
		}
	}
	if cfg.MarketDataStore != nil {
		cfg.MarketDataStore.SetTDXDailyDirectory(cfg.TDXDailyDirectory)
		cfg.KLinePrimary = &marketFirstKLineProvider{
			store:         cfg.MarketDataStore,
			international: international,
			primary:       cfg.KLinePrimary,
			fallback:      cfg.KLineFallback,
			logger:        cfg.Logger,
		}
		cfg.KLineFallback = nil
	}
	var tdxClient *tdx.Client
	if cfg.MarketDataDBPath != "" && filepath.IsAbs(cfg.MarketDataDBPath) && os.Getenv("A_STOCK_TDX_ENABLED") != "false" {
		var hosts []string
		if raw := strings.TrimSpace(os.Getenv("A_STOCK_TDX_SERVERS")); raw != "" {
			hosts = strings.Split(raw, ",")
		}
		var err error
		tdxClient, err = tdx.New(filepath.Join(filepath.Dir(cfg.MarketDataDBPath), "tdx-cache.db"), hosts)
		if err != nil {
			startupErrors = append(startupErrors, fmt.Errorf("open TDX external cache: %w", err))
		} else {
			if usingDefaultKLine {
				cfg.KLinePrimary = &tdxFirstKLineProvider{
					tdx: tdxClient, fallback: cfg.KLinePrimary,
					preferLocalDaily: cfg.MarketDataStore != nil && cfg.MarketDataStore.TDXDailyEnabled(),
				}
			}
		}
	}
	if cfg.ReviewStore == nil {
		store, err := review.OpenStore(cfg.ReviewDBPath)
		if err == nil {
			cfg.ReviewStore = store
		} else if cfg.StrictPersistence {
			startupErrors = append(startupErrors, fmt.Errorf("open review database: %w", err))
			cfg.ReviewStore, _ = review.OpenStore(":memory:")
		} else {
			cfg.ReviewStore, _ = review.OpenStore(":memory:")
		}
	}
	if cfg.MarketEmotionStore == nil {
		store, err := marketemotion.OpenStore(cfg.MarketEmotionDBPath)
		if err == nil {
			cfg.MarketEmotionStore = store
		} else if cfg.StrictPersistence {
			startupErrors = append(startupErrors, fmt.Errorf("open market emotion database: %w", err))
			cfg.MarketEmotionStore, _ = marketemotion.OpenStore("")
		} else {
			cfg.MarketEmotionStore, _ = marketemotion.OpenStore("")
		}
	}
	if cfg.PortfolioStore == nil {
		store, err := portfolioinspection.OpenStore(cfg.PortfolioDBPath)
		if err == nil {
			cfg.PortfolioStore = store
		} else if cfg.StrictPersistence {
			startupErrors = append(startupErrors, fmt.Errorf("open portfolio inspection database: %w", err))
			cfg.PortfolioStore, _ = portfolioinspection.OpenStore(":memory:")
		} else {
			cfg.PortfolioStore, _ = portfolioinspection.OpenStore(":memory:")
		}
	}
	if cfg.StockResearchStore == nil {
		store, err := stockanalysis.OpenResearchStore(cfg.StockResearchDBPath)
		if err != nil {
			if cfg.StrictPersistence {
				startupErrors = append(startupErrors, fmt.Errorf("open stock research database: %w", err))
			}
			store, _ = stockanalysis.OpenResearchStore(":memory:")
		}
		cfg.StockResearchStore = store
	}
	if cfg.ReviewHTTP == nil {
		cfg.ReviewHTTP = &http.Client{Timeout: 90 * time.Second}
	}
	if cfg.SettingsStore == nil {
		store, err := appsettings.Open(cfg.SettingsPath)
		if err == nil {
			cfg.SettingsStore = store
		} else if cfg.StrictPersistence {
			startupErrors = append(startupErrors, fmt.Errorf("open settings: %w", err))
			cfg.SettingsStore, _ = appsettings.Open("")
		} else {
			cfg.SettingsStore, _ = appsettings.Open("")
		}
	}
	if cfg.HotStocks == nil {
		cfg.HotStocks = hotstock.NewClient(hotstock.WithHTTPClient(newMarketHTTPClient(12*time.Second, cfg.MarketCacheTransport)))
	}
	if cfg.FuturesPosition == nil {
		cfg.FuturesPosition = futurespositionprovider.NewClient(futurespositionprovider.WithHTTPClient(newMarketHTTPClient(8*time.Second, cfg.MarketCacheTransport)))
	}
	if cfg.HermesGateway != nil && (!cfg.StrictPersistence || len(startupErrors) == 0) {
		values := cfg.SettingsStore.Snapshot()
		var migratedKey *string
		if strings.TrimSpace(values.LLM.APIKey) != "" {
			key := strings.TrimSpace(values.LLM.APIKey)
			migratedKey = &key
			if updated, err := cfg.SettingsStore.Update(func(next *appsettings.Values) error {
				next.LLM.APIKey = ""
				return nil
			}); err == nil {
				values = updated
			}
		}
		if profileGateway, ok := cfg.HermesGateway.(hermes.ProfileGateway); ok {
			if migratedKey == nil {
				if key, err := cfg.HermesGateway.ModelAPIKey(); err == nil && strings.TrimSpace(key) != "" {
					migratedKey = &key
				}
			}
			_ = profileGateway.SyncLLMProfile(values.LLM, values.ActiveLLMProfileID, migratedKey)
		} else {
			_ = cfg.HermesGateway.SyncLLM(values.LLM, migratedKey)
		}
	}
	usageGateway := newTokenUsageGateway(cfg.HermesGateway, tokenUsage)
	if cfg.ReviewImporter == nil {
		cfg.ReviewImporter = review.NewImporter(cfg.ReviewHTTP, cfg.WeChatAPIURL)
	}
	if cfg.ReviewAutomation == nil {
		cfg.ReviewAutomation = review.NewAutomation(cfg.ReviewStore, cfg.ReviewImporter, cfg.SettingsStore, cfg.ReviewHTTP, cfg.WeChatAPIURL, usageGateway)
	}
	if dailyMarketProvider := newReviewDailyMarketProvider(cfg.MarketOverview); dailyMarketProvider != nil {
		cfg.ReviewAutomation.SetDailyMarketProvider(dailyMarketProvider)
	}
	if cfg.RemoteDailySync == nil {
		cfg.RemoteDailySync = review.NewRemoteDailySync(cfg.ReviewStore, review.RemoteDailySyncConfig{
			BaseURL: cfg.RemoteDailyReviewURL,
			Client:  cfg.ReviewHTTP,
		})
	}
	s := &Server{
		mux:                   http.NewServeMux(),
		marketNetwork:         cfg.MarketNetwork,
		sourceHealth:          sourceHealth,
		token:                 cfg.Token,
		realtimeProvider:      cfg.Realtime,
		kLinePrimary:          cfg.KLinePrimary,
		tdx:                   tdxClient,
		kLineFallback:         cfg.KLineFallback,
		autoFollowDBPath:      cfg.AutoFollowDBPath,
		newsProvider:          cfg.News,
		sectorMap:             cfg.SectorMap,
		themeOverview:         cfg.ThemeOverview,
		limitUpProvider:       cfg.LimitUp,
		marketPools:           cfg.MarketPools,
		stockConcepts:         cfg.StockConcept,
		stockBusiness:         cfg.StockBusiness,
		stockDirectory:        cfg.StockDirectory,
		hotStockProvider:      cfg.HotStocks,
		futuresPosition:       cfg.FuturesPosition,
		marketOverview:        cfg.MarketOverview,
		inflection:            cfg.Inflection,
		themeSnapshots:        newThemeSnapshotCache(30 * time.Minute),
		themeProgress:         newThemeProgressCache(),
		limitUpSnapshots:      newLimitUpLadderCache(30 * time.Second),
		limitUpProgress:       &shortTermCache[limitUpLadderData]{},
		emotionProgress:       &shortTermCache[marketemotion.History]{},
		stockDirectories:      newStockDirectoryCache(6 * time.Hour),
		hotStockRanks:         newHotStockRankCache(30 * time.Minute),
		marketSnapshots:       newMarketOverviewCache(45 * time.Second),
		marketEmotionIntraday: newMarketEmotionIntradayCache(marketEmotionIntradayTTL),
		reviewStore:           cfg.ReviewStore,
		portfolioStore:        cfg.PortfolioStore,
		stockResearchStore:    cfg.StockResearchStore,
		reviewImporter:        cfg.ReviewImporter,
		wechatAPIURL:          strings.TrimSpace(cfg.WeChatAPIURL),
		settingsStore:         cfg.SettingsStore,
		ladderThemeAI:         newLadderThemeAI(cfg.SettingsPath),
		reviewAutomation:      cfg.ReviewAutomation,
		remoteDailySync:       cfg.RemoteDailySync,
		hermesGateway:         cfg.HermesGateway,
		usageGateway:          usageGateway,
		masteryLibrary:        cfg.MasteryLibrary,
		marketEmotionStore:    cfg.MarketEmotionStore,
		marketBars:            cfg.MarketDataStore,
		liveMarketRuntime:     newLiveMarketRuntimeState(),
		startedAt:             time.Now(),
		startupError:          errors.Join(startupErrors...),
		logger:                cfg.Logger,
		tokenUsage:            tokenUsage,
	}
	if cfg.WebAuthDBPath != "" {
		var err error
		s.webAuth, err = openWebAuth(cfg.WebAuthDBPath)
		s.startupError = errors.Join(s.startupError, err)
	}
	if strings.TrimSpace(cfg.LinkageDBPath) != "" {
		service, err := linkage.NewService(cfg.LinkageDBPath, cfg.LinkageTargetsPath)
		if err != nil {
			cfg.Logger.Printf("level=warn event=linkage_unavailable error=%q", err)
		} else {
			s.linkage = service
		}
	}
	if kaipanlaService != nil {
		s.themeRadarStore = kaipanlaService.Store()
		if payload, err := s.themeRadarStore.LoadOverview(context.Background()); err == nil && len(payload) > 0 {
			var cached foundation.ThemeProgress
			if json.Unmarshal(payload, &cached) == nil {
				cached.Refreshing = false
				cached.Meta.Stale = true
				s.themeProgress.value = cached
			}
		}
	}
	if s.themeRadarStore != nil {
		if payload, err := s.themeRadarStore.LoadLadder(context.Background()); err == nil && len(payload) > 0 {
			var cached shortTermProgress[limitUpLadderData]
			if json.Unmarshal(payload, &cached) == nil && cached.Data != nil {
				cached.Refreshing, cached.Stale = false, true
				cached.Data.Meta.Stale = true
				if cached.Data.Intraday != nil {
					cached.Data.Intraday.Stale = true
				}
				s.limitUpProgress.value = cached
			}
		}
	}
	s.marketEmotion = newMarketEmotionEngine(
		cfg.MarketEmotionStore,
		s.limitUpProvider,
		s.marketPools,
		s.kLinePrimary,
		s.kLineFallback,
		s.stockConcepts,
	)
	s.stockResearch = stockanalysis.NewResearchService(cfg.StockResearchStore, s.runStockResearch)
	s.portfolioInspection = portfolioinspection.NewService(cfg.PortfolioStore, usageGateway, s.analyzeStock, cfg.Logger, s.analyzeHoldingResearch)
	s.portfolioInspection.SetDesktopAdapter(portfolioinspection.DesktopAdapter{
		Validate:       s.validatePortfolioDesktopRuntime,
		AnalyzeHolding: s.analyzePortfolioHolding,
		GenerateReport: s.generatePortfolioDesktopReport,
	})
	s.portfolioExpectation = portfolioinspection.NewExpectationService(cfg.PortfolioStore, cfg.ReviewStore, usageGateway, s.analyzeStock, cfg.Logger, s.analyzeHoldingResearch)
	s.routes()
	return s
}

func (s *Server) StartupError() error {
	if s == nil {
		return errors.New("server is nil")
	}
	return s.startupError
}

func (s *Server) Close() error {
	if s != nil && s.themeProgress != nil {
		s.themeProgress.close()
	}
	if s == nil {
		return nil
	}
	if s.limitUpProgress != nil {
		s.limitUpProgress.close()
	}
	if s.emotionProgress != nil {
		s.emotionProgress.close()
	}
	s.closeSystemStatusCache()
	var closeErrors []error
	if s.webAuth != nil {
		closeErrors = append(closeErrors, s.webAuth.db.Close())
	}
	if s.linkage != nil {
		closeErrors = append(closeErrors, s.linkage.Close())
	}
	if s.tdx != nil {
		closeErrors = append(closeErrors, s.tdx.Close())
	}
	if s.stockResearch != nil {
		s.stockResearch.Close()
	}
	s.desktopSignals.close()
	s.desktopDBMu.Lock()
	if s.desktopChatDB != nil {
		closeErrors = append(closeErrors, s.desktopChatDB.Close())
		s.desktopChatDB = nil
	}
	s.desktopDBMu.Unlock()
	if s.stockResearchStore != nil {
		closeErrors = append(closeErrors, s.stockResearchStore.Close())
	}
	if s.reviewStore != nil {
		closeErrors = append(closeErrors, s.reviewStore.Close())
	}
	if s.marketEmotionStore != nil {
		closeErrors = append(closeErrors, s.marketEmotionStore.Close())
	}
	if s.portfolioStore != nil {
		closeErrors = append(closeErrors, s.portfolioStore.Close())
	}
	if s.themeRadarStore != nil {
		closeErrors = append(closeErrors, s.themeRadarStore.Close())
	}
	if s.marketBars != nil {
		closeErrors = append(closeErrors, s.marketBars.Close())
	}
	return errors.Join(closeErrors...)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if marketcache.RefreshRequested(r.Header) {
		r = r.WithContext(marketcache.WithRefresh(r.Context()))
	}
	startedAt := time.Now()
	requestID := runtimeRequestID(r)
	loggedWriter := &requestLogWriter{ResponseWriter: w}
	loggedWriter.Header().Set("X-Request-ID", requestID)
	defer s.logRequest(r, loggedWriter, requestID, startedAt)

	isLinkage := strings.HasPrefix(r.URL.Path, "/api/v1/linkage/")
	s.withCORS(loggedWriter, r)
	if r.Method == http.MethodOptions {
		loggedWriter.WriteHeader(http.StatusNoContent)
		return
	}
	if s.webAuth != nil && s.handleWebAuth(loggedWriter, r) {
		return
	}
	setupImport := r.Method == http.MethodPost && r.URL.Path == "/api/v1/linkage/targets/import"
	if setupImport && !s.authorizedLinkageAdmin(r) {
		writeError(loggedWriter, http.StatusUnauthorized, "authorized setup token required")
		return
	}
	if isLinkage && !setupImport && !s.authorizedLinkage(r) || !isLinkage && !s.authorized(r) {
		status := http.StatusUnauthorized
		if s.webAuth != nil {
			if _, ok := s.webAuth.user(r); ok {
				status = http.StatusForbidden
			}
		}
		writeError(loggedWriter, status, "unauthorized")
		return
	}
	s.mux.ServeHTTP(loggedWriter, r)
}

func (s *Server) RunReviewScheduler(ctx context.Context) {
	if s.reviewAutomation != nil {
		s.logSchedulerLifecycle(ctx, "reviews", "subscription_sync", func() {
			s.reviewAutomation.RunScheduler(ctx, s.logger)
		})
	}
}

func (s *Server) RunRemoteDailyReviewScheduler(ctx context.Context) {
	if s.remoteDailySync != nil {
		s.logSchedulerLifecycle(ctx, "reviews", "remote_daily_sync", func() {
			s.remoteDailySync.Run(ctx, s.logger)
		})
	}
}

func (s *Server) RunMarketEmotionScheduler(ctx context.Context) {
	if s.marketEmotion != nil {
		s.logSchedulerLifecycle(ctx, "short-term", "market_emotion", func() {
			s.marketEmotion.runScheduler(ctx, s.logger)
		})
	}
}

func (s *Server) RunMasteryScheduler(ctx context.Context) {
	if s.masteryLibrary == nil {
		return
	}
	if s.logger != nil {
		s.logger.Printf("level=info event=scheduler_start feature=trading-mastery task=mastery_snapshot")
		defer s.logger.Printf("level=info event=scheduler_stop feature=trading-mastery task=mastery_snapshot")
	}
	s.refreshMasterySnapshot(ctx, false)
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshMasterySnapshot(ctx, true)
		}
	}
}

func (s *Server) logSchedulerLifecycle(_ context.Context, feature, task string, run func()) {
	if s.logger != nil {
		s.logger.Printf("level=info event=scheduler_start feature=%s task=%s", feature, task)
		defer s.logger.Printf("level=info event=scheduler_stop feature=%s task=%s", feature, task)
	}
	run()
}

func (s *Server) refreshMasterySnapshot(ctx context.Context, force bool) {
	startedAt := time.Now()
	_, err := s.masteryLibrary.Snapshot(ctx, force)
	if s.logger == nil {
		return
	}
	if err != nil {
		s.logger.Printf("level=warn event=scheduler_error feature=trading-mastery task=mastery_snapshot force=%t duration_ms=%d error=%q", force, time.Since(startedAt).Milliseconds(), runtimelog.Redact(err.Error()))
		return
	}
	s.logger.Printf("level=info event=scheduler_run feature=trading-mastery task=mastery_snapshot force=%t duration_ms=%d", force, time.Since(startedAt).Milliseconds())
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/v1/linkage/targets", s.linkageTargets)
	s.mux.HandleFunc("PUT /api/v1/linkage/default", s.linkageDefaultUpdate)
	s.mux.HandleFunc("POST /api/v1/linkage/targets/import", s.linkageImportTarget)
	s.mux.HandleFunc("POST /api/v1/linkage/commands", s.linkageSubmit)
	s.mux.HandleFunc("GET /api/v1/linkage/operations/{operation_id}", s.linkageOperation)
	s.mux.HandleFunc("GET /api/v1/linkage/context", s.linkageContextHandler)
	s.mux.HandleFunc("POST /api/v1/linkage/context/evidence", s.linkageEvidenceHandler)
	s.mux.HandleFunc("GET /api/v1/linkage/events", s.linkageEventsHandler)
	s.mux.HandleFunc("GET /api/v1/linkage/attention", s.linkageAttentionSnapshot)
	s.mux.HandleFunc("POST /api/v1/linkage/attention/intents", s.linkageAttentionIntent)
	s.mux.HandleFunc("POST /api/v1/linkage/attention/intents/batch", s.linkageAttentionBatch)
	s.mux.HandleFunc("GET /api/health", s.health)
	s.mux.HandleFunc("GET /api/v1/system/status", s.systemStatus)
	s.mux.HandleFunc("GET /api/v1/auto-follow", s.autoFollowStatus)
	for _, operation := range []string{"claim", "result", "heartbeat", "chat_claim", "chat_result", "next", "lease"} {
		s.mux.HandleFunc("POST /api/v1/linkage/strategy/"+operation, s.autoFollowAIPeer)
	}
	s.mux.HandleFunc("POST /api/v1/auto-follow/commands", s.autoFollowCommand)
	s.mux.HandleFunc("GET /api/v1/market-data/network", s.marketNetworkSettings)
	s.mux.HandleFunc("PUT /api/v1/market-data/network", s.marketNetworkSettings)
	s.mux.HandleFunc("GET /api/v1/sources", s.sources)
	s.mux.HandleFunc("GET /api/v1/quotes/realtime", s.realtime)
	s.mux.HandleFunc("GET /api/v1/quotes/tdx/bars", s.tdxBars)
	s.mux.HandleFunc("GET /api/v1/quotes/kline", s.kline)
	s.mux.HandleFunc("GET /api/v1/quotes/kline/batch", s.klineBatch)
	s.mux.HandleFunc("POST /api/v1/quotes/sbc/v1/analyze", s.sbcAnalysis)
	s.mux.HandleFunc("GET /api/v1/market-data/stocks", s.marketDataStocks)
	s.mux.HandleFunc("POST /api/v1/market-data/stocks", s.marketDataStockAdd)
	s.mux.HandleFunc("GET /api/v1/market-data/stocks/{market}/{symbol}/bars", s.marketDataStockBars)
	s.mux.HandleFunc("POST /api/v1/market-data/stocks/{market}/{symbol}/refresh", s.marketDataStockRefresh)
	s.mux.HandleFunc("DELETE /api/v1/market-data/stocks/{market}/{symbol}", s.marketDataStockRemove)
	s.mux.HandleFunc("DELETE /api/v1/market-data/stocks/{market}/{symbol}/cache", s.marketDataStockCacheClear)
	s.mux.HandleFunc("GET /api/v1/market/news", s.news)
	s.mux.HandleFunc("GET /api/v1/market/indexes", s.marketIndexesHandler)
	s.mux.HandleFunc("GET /api/v1/market/index-series", s.marketIndexSeriesHandler)
	s.mux.HandleFunc("GET /api/v1/market/industries", s.marketIndustriesHandler)
	s.mux.HandleFunc("GET /api/v1/market/flows", s.marketFlowsHandler)
	s.mux.HandleFunc("GET /api/v1/market/margin-balance", s.marketMarginBalanceHandler)
	s.mux.HandleFunc("GET /api/v1/market/billboard", s.marketBillboardHandler)
	s.mux.HandleFunc("GET /api/v1/market/billboard/detail", s.marketBillboardDetailHandler)
	s.mux.HandleFunc("GET /api/v1/market/futures-position", s.marketFuturesPositionHandler)
	s.mux.HandleFunc("GET /api/v1/market/futures-members", s.marketFuturesMembersHandler)
	s.mux.HandleFunc("GET /api/v1/market/futures-consensus", s.marketFuturesConsensusHandler)
	s.mux.HandleFunc("GET /api/v1/research/announcements", s.marketAnnouncementsHandler)
	s.mux.HandleFunc("GET /api/v1/research/institution-reports", s.marketInstitutionReportsHandler)
	s.mux.HandleFunc("GET /api/v1/research/industries", s.marketIndustryResearchHandler)
	s.mux.HandleFunc("GET /api/v1/themes/overview", s.themeOverviewHandler)
	s.mux.HandleFunc("GET /api/v1/themes/screen", s.themeScreenHandler)
	s.mux.HandleFunc("GET /api/v1/sector-map", s.sectorMapHandler)
	s.mux.HandleFunc("GET /api/v1/short-term/limit-up-ladder", s.limitUpLadderHandler)
	s.mux.HandleFunc("POST /api/v1/short-term/ladder-theme-ai", s.ladderThemeAIIdentify)
	s.mux.HandleFunc("GET /api/v1/short-term/ladder-theme-ai", s.ladderThemeAIResults)
	s.mux.HandleFunc("GET /api/v1/short-term/emotion-history", s.marketEmotionHistoryHandler)
	s.mux.HandleFunc("GET /api/v1/short-term/mastery", s.masteryIndex)
	s.mux.HandleFunc("GET /api/v1/short-term/mastery/trader", s.masteryTrader)
	s.mux.HandleFunc("POST /api/v1/short-term/mastery/refresh", s.masteryRefresh)
	s.mux.HandleFunc("POST /api/v1/stocks/ai-analysis", s.stockAIAnalysis)
	s.mux.HandleFunc("POST /api/v1/stocks/research", s.stockResearchCreate)
	s.mux.HandleFunc("GET /api/v1/stocks/research", s.stockResearchList)
	s.mux.HandleFunc("GET /api/v1/stocks/research/{id}", s.stockResearchGet)
	s.mux.HandleFunc("GET /api/v1/stocks/research/{id}/events", s.stockResearchEvents)
	s.mux.HandleFunc("DELETE /api/v1/stocks/research/{id}", s.stockResearchDelete)
	s.mux.HandleFunc("POST /api/v1/stocks/research/{id}/cancel", s.stockResearchCancel)
	s.mux.HandleFunc("POST /api/v1/stocks/research/{id}/verify", s.stockResearchVerify)
	s.mux.HandleFunc("GET /api/v1/stocks/research/{id}/snapshot", s.stockResearchSnapshot)
	s.mux.HandleFunc("GET /api/v1/stocks/directory", s.stockDirectoryHandler)
	s.mux.HandleFunc("GET /api/v1/stocks/hot-ranks", s.hotStockRanksHandler)
	s.mux.HandleFunc("GET /api/v1/portfolio-inspections", s.portfolioInspectionList)
	s.mux.HandleFunc("POST /api/v1/portfolio-inspections", s.portfolioInspectionCreate)
	s.mux.HandleFunc("GET /api/v1/portfolio-inspections/{id}", s.portfolioInspectionGet)
	s.mux.HandleFunc("DELETE /api/v1/portfolio-inspections/{id}", s.portfolioInspectionDelete)
	s.mux.HandleFunc("POST /api/v1/reviews/portfolio-expectations", s.portfolioExpectationCreate)
	s.mux.HandleFunc("GET /api/v1/reviews/portfolio-expectations/latest", s.portfolioExpectationLatest)
	s.mux.HandleFunc("GET /api/v1/reviews/portfolio-expectations/{id}", s.portfolioExpectationGet)
	s.mux.HandleFunc("GET /api/v1/reviews/sources", s.reviewSources)
	s.mux.HandleFunc("GET /api/v1/reviews/authors", s.reviewAuthors)
	s.mux.HandleFunc("DELETE /api/v1/reviews/authors/{id}", s.reviewAuthorDelete)
	s.mux.HandleFunc("GET /api/v1/reviews/posts", s.reviewPosts)
	s.mux.HandleFunc("GET /api/v1/reviews/posts/{id}", s.reviewPost)
	s.mux.HandleFunc("DELETE /api/v1/reviews/posts/{id}", s.reviewPostDelete)
	s.mux.HandleFunc("GET /api/v1/reviews/daily-summary", s.reviewDailySummaryGet)
	s.mux.HandleFunc("POST /api/v1/reviews/daily-summary/anonymize", s.reviewDailySummaryAnonymize)
	s.mux.HandleFunc("GET /api/v1/reviews/daily-summary/window", s.reviewDailySummaryWindow)
	s.mux.HandleFunc("GET /api/v1/reviews/daily-summary/status", s.reviewDailySummaryStatus)
	s.mux.HandleFunc("POST /api/v1/reviews/daily-summary", s.reviewDailySummaryCreate)
	s.mux.HandleFunc("GET /api/v1/reviews/daily-validation", s.reviewDailyValidation)
	s.mux.HandleFunc("GET /api/v1/reviews/daily-validation/status", s.reviewDailyValidationStatus)
	s.mux.HandleFunc("POST /api/v1/reviews/daily-validation", s.reviewDailyValidationCreate)
	s.mux.HandleFunc("POST /api/v1/reviews/import", s.reviewImport)
	s.mux.HandleFunc("GET /api/v1/reviews/subscriptions", s.reviewSubscriptions)
	s.mux.HandleFunc("POST /api/v1/reviews/subscriptions", s.reviewSubscriptionCreate)
	s.mux.HandleFunc("DELETE /api/v1/reviews/subscriptions/{id}", s.reviewSubscriptionDelete)
	s.mux.HandleFunc("POST /api/v1/reviews/sync", s.reviewSyncAll)
	s.mux.HandleFunc("POST /api/v1/reviews/subscriptions/{id}/sync", s.reviewSyncOne)
	s.mux.HandleFunc("POST /api/v1/reviews/posts/{id}/analyze", s.reviewAnalyzePost)
	s.mux.HandleFunc("GET /api/v1/reviews/remote-daily/status", s.reviewRemoteDailyStatus)
	s.mux.HandleFunc("POST /api/v1/reviews/remote-daily/sync", s.reviewRemoteDailySync)
	s.mux.HandleFunc("GET /api/v1/settings", s.settingsGet)
	s.mux.HandleFunc("GET /api/v1/settings/token-usage", s.tokenUsageSummary)
	s.mux.HandleFunc("POST /api/v1/settings/token-usage", s.tokenUsageRecord)
	s.mux.HandleFunc("PUT /api/v1/settings", s.settingsUpdate)
	s.mux.HandleFunc("GET /api/v1/settings/agent", s.settingsAgentGet)
	s.mux.HandleFunc("PUT /api/v1/settings/agent", s.settingsAgentUpdate)
	s.mux.HandleFunc("POST /api/v1/settings/agent/skills/import", s.settingsAgentSkillImport)
	s.mux.HandleFunc("POST /api/v1/settings/agent/skills/delete", s.settingsAgentSkillDelete)
	s.mux.HandleFunc("POST /api/v1/settings/agent/skills/install-git", s.settingsAgentSkillInstallGit)
	s.mux.HandleFunc("GET /api/v1/settings/agent/skills/market", s.settingsAgentSkillMarket)
	s.mux.HandleFunc("GET /api/v1/settings/agent/skills/market/sources", s.settingsAgentSkillMarketSources)
	s.mux.HandleFunc("POST /api/v1/settings/llm/models", s.settingsLLMModels)
	s.mux.HandleFunc("POST /api/v1/settings/llm/test", s.settingsLLMTest)
	s.mux.HandleFunc("GET /api/v1/ai/ws", s.aiChatWebSocket)
	s.mux.HandleFunc("GET /api/v1/ai/runtime", s.desktopChatRuntime)
	s.mux.HandleFunc("GET /api/v1/ai/auto-research", s.automaticResearch)
	s.mux.HandleFunc("PUT /api/v1/ai/auto-research", s.automaticResearch)
	s.mux.HandleFunc("POST /api/v1/ai/auto-research", s.settleAutomaticResearchManual)
	s.mux.HandleFunc("POST /api/v1/ai/auto-research/settle", s.settleAutomaticResearchManual)
	s.mux.HandleFunc("PUT /api/v1/ai/runtime", s.desktopChatRuntime)
	s.mux.HandleFunc("POST /api/v1/ai/desktop/jobs", s.desktopChatJob)
	s.mux.HandleFunc("GET /api/v1/ai/desktop/jobs/{id}", s.desktopChatJob)
	s.mux.HandleFunc("GET /api/v1/ai/desktop/jobs/{id}/events", s.desktopChatEvents)
	s.mux.HandleFunc("DELETE /api/v1/ai/desktop/jobs/{id}", s.desktopChatJob)
	s.mux.HandleFunc("POST /api/v1/strategy/inflections/evaluate", s.inflectionEvaluate)
	s.mux.HandleFunc("GET /api/v1/ws/stream", s.stream)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"name": "easy-stock data foundation",
		"time": time.Now(),
	})
}

func (s *Server) sources(w http.ResponseWriter, r *http.Request) {
	monitor := s.sourceHealth
	if monitor == nil {
		monitor = newSourceHealthMonitor(nil)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": monitor.snapshot(time.Now())})
}

func (s *Server) realtime(w http.ResponseWriter, r *http.Request) {
	symbolsParam := strings.TrimSpace(r.URL.Query().Get("symbols"))
	if symbolsParam == "" {
		writeError(w, http.StatusBadRequest, "symbols is required")
		return
	}
	symbols, err := foundation.SplitSymbols(symbolsParam)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	quotes, err := s.realtimeProvider.Realtime(r.Context(), symbols)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": quotes})
}

func (s *Server) kline(w http.ResponseWriter, r *http.Request) {
	symbol := strings.TrimSpace(r.URL.Query().Get("symbol"))
	if symbol == "" {
		writeError(w, http.StatusBadRequest, "symbol is required")
		return
	}
	period := firstNonEmpty(r.URL.Query().Get("period"), "day")
	limit := 120
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = parsed
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if market := strings.TrimSpace(r.URL.Query().Get("market")); market != "" {
		ctx = marketbars.WithMarket(ctx, market)
	}
	lines, err := s.loadKLine(ctx, symbol, period, limit)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": lines})
}

func (s *Server) klineBatch(w http.ResponseWriter, r *http.Request) {
	symbolsParam := strings.TrimSpace(r.URL.Query().Get("symbols"))
	if symbolsParam == "" {
		writeError(w, http.StatusBadRequest, "symbols is required")
		return
	}
	symbols, err := foundation.SplitSymbols(symbolsParam)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(symbols) > 30 {
		writeError(w, http.StatusBadRequest, "batch kline supports at most 30 symbols")
		return
	}
	period := firstNonEmpty(r.URL.Query().Get("period"), "day")
	limit := 40
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed <= 0 || parsed > 240 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 240")
			return
		}
		limit = parsed
	}

	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	if market := strings.TrimSpace(r.URL.Query().Get("market")); market != "" {
		ctx = marketbars.WithMarket(ctx, market)
	}
	data := make(map[string][]foundation.KLine, len(symbols))
	errorsBySymbol := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 4)
	for _, symbol := range symbols {
		symbol := symbol
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				mu.Lock()
				errorsBySymbol[symbol] = ctx.Err().Error()
				mu.Unlock()
				return
			}
			lines, loadErr := s.loadKLine(ctx, symbol, period, limit)
			mu.Lock()
			defer mu.Unlock()
			if loadErr != nil {
				errorsBySymbol[symbol] = loadErr.Error()
				return
			}
			data[symbol] = lines
		}()
	}
	wg.Wait()
	if len(data) == 0 {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "all batch kline requests failed", "errors": errorsBySymbol})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "errors": errorsBySymbol})
}

func (s *Server) loadKLine(ctx context.Context, symbol string, period string, limit int) ([]foundation.KLine, error) {
	if period == "2d" || period == "3d" {
		days := 2
		if period == "3d" {
			days = 3
		}
		lines, err := s.loadKLine(ctx, symbol, "day", limit*days)
		if err != nil {
			return nil, err
		}
		return lastKLines(aggregateTradingDays(lines, days), limit), nil
	}
	lines, err := s.kLinePrimary.KLine(ctx, symbol, period, limit)
	if err == nil {
		return normalizeKLinePeriod(lines, period), nil
	}
	if s.kLineFallback == nil {
		return nil, err
	}
	lines, err = s.kLineFallback.KLine(ctx, symbol, period, limit)
	if err != nil {
		return nil, err
	}
	return normalizeKLinePeriod(lines, period), nil
}

func normalizeKLinePeriod(lines []foundation.KLine, period string) []foundation.KLine {
	if strings.TrimSpace(period) != "1" || len(lines) == 0 {
		return lines
	}

	chinaTime := time.FixedZone("Asia/Shanghai", 8*60*60)
	latestTime := time.Time{}
	for _, line := range lines {
		if !line.Time.IsZero() && line.Time.After(latestTime) {
			latestTime = line.Time
		}
	}
	if latestTime.IsZero() {
		return lines
	}

	latestDate := latestTime.In(chinaTime).Format("2006-01-02")
	previousTime := time.Time{}
	previousClose := 0.0
	for _, line := range lines {
		if line.Time.IsZero() || line.Time.In(chinaTime).Format("2006-01-02") == latestDate {
			continue
		}
		if line.Close > 0 && line.Time.Before(latestTime) && line.Time.After(previousTime) {
			previousTime = line.Time
			previousClose = line.Close
		}
	}

	filtered := make([]foundation.KLine, 0, len(lines))
	for _, line := range lines {
		if line.Time.IsZero() || line.Time.In(chinaTime).Format("2006-01-02") != latestDate {
			continue
		}
		if line.PreviousClose <= 0 && previousClose > 0 {
			line.PreviousClose = previousClose
		}
		filtered = append(filtered, line)
	}
	if len(filtered) == 0 {
		return lines
	}
	return intradayVWAP(filtered)
}

func (s *Server) news(w http.ResponseWriter, r *http.Request) {
	source := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source")))
	if source == "" {
		source = "cls"
	}
	if source != "cls" {
		writeError(w, http.StatusBadRequest, "unsupported news source")
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = parsed
	}
	items, err := s.newsProvider.LatestNews(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items})
}

func (s *Server) sectorMapHandler(w http.ResponseWriter, r *http.Request) {
	theme := strings.TrimSpace(r.URL.Query().Get("theme"))
	if theme == "" {
		theme = "semiconductor_materials"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	sectorMap, err := s.sectorMap.Build(ctx, theme)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": sectorMap})
}

func (s *Server) themeOverviewHandler(w http.ResponseWriter, r *http.Request) {
	if s.themeOverview == nil {
		writeError(w, http.StatusServiceUnavailable, "theme overview provider is unavailable")
		return
	}
	if r.URL.Query().Get("delivery") == "progressive" {
		s.progressiveThemeOverview(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	items, meta, err := s.themeOverview.Overviews(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "meta": meta})
}

func (s *Server) inflectionEvaluate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request inflection.EvaluationRequest
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid inflection request: "+err.Error())
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.inflection.Evaluate(request)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

func writeError(w http.ResponseWriter, status int, message string) {
	if status >= http.StatusInternalServerError {
		log.Printf("level=error event=http_error status=%d message=%q", status, runtimelog.Redact(message))
	}
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
