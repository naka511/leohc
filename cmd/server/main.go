package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"leo-go/internal/config"
	"leo-go/internal/handler"
	"leo-go/internal/provider/leonardo"
	"leo-go/internal/reqlog"
	"leo-go/internal/store"
	"leo-go/internal/token"
)

func main() {
	port := flag.Int("port", 8787, "server port")
	host := flag.String("host", "0.0.0.0", "bind address")
	configPath := flag.String("config", "", "config file path")
	flag.Parse()

	baseDir, _ := os.Getwd()
	configDir := filepath.Join(baseDir, "config")
	os.MkdirAll(configDir, 0o755)
	staticDir := filepath.Join(baseDir, "static")
	generatedDir := filepath.Join(baseDir, "generated")
	os.MkdirAll(generatedDir, 0o755)

	// Load config
	cfg := config.Global()
	cfgFile := *configPath
	if cfgFile == "" {
		cfgFile = filepath.Join(configDir, "config.json")
	}
	if err := cfg.Load(cfgFile); err != nil {
		log.Printf("[config] warning: %v", err)
	}
	log.Printf("[config] loaded from %s", cfgFile)

	// SQLite store
	dbPath := filepath.Join(configDir, "app.db")
	sqlStore, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		log.Fatalf("[store] failed to open database: %v", err)
	}
	defer sqlStore.Close()

	var redisStore *store.RedisStore
	if candidate, redisErr := store.NewRedisStoreFromEnv(); redisErr != nil {
		log.Printf("[redis] disabled: %v", redisErr)
	} else if candidate != nil {
		if pingErr := candidate.Ping(); pingErr != nil {
			log.Printf("[redis] unavailable, fallback to local storage: %v", pingErr)
		} else {
			redisStore = candidate
			log.Printf("[redis] connected: addr=%s db=%d prefix=%s", redisStore.Address(), redisStore.DB(), redisStore.KeyPrefix())
			if seedErr := seedRedisTokens(redisStore, sqlStore); seedErr != nil {
				log.Printf("[redis] failed to seed tokens from sqlite: %v", seedErr)
			}
		}
	}

	// Token manager
	var tokenStore store.TokenStore = sqlStore
	if redisStore != nil {
		tokenStore = redisStore
	}
	tokenMgr := token.NewManager(tokenStore)
	log.Printf("[token] pool ready: %d tokens loaded", tokenMgr.Count())

	// Leonardo client
	proxy := ""
	if cfg.GetBool("use_proxy", false) {
		proxy = cfg.GetString("proxy", "")
	}
	leoClient := leonardo.NewClient(proxy)
	leoClient.SetJWTRefreshMarginMinutes(cfg.GetInt("jwt_refresh_margin_minutes", 5))
	log.Printf("[leonardo] client initialized")

	reqLogFile := filepath.Join(configDir, "request_logs.json")
	reqLogStore := reqlog.NewStore(reqLogFile)
	if redisStore != nil {
		reqLogStore = reqlog.NewStoreWithJSON(reqLogFile, redisStore, "request_logs")
	}
	if expired := reqLogStore.ExpireStaleRunning(time.Duration(cfg.GetInt("generate_timeout", 600))*time.Second, time.Now()); expired > 0 {
		log.Printf("[reqlog] expired %d stale running log(s) during startup", expired)
	}

	srv := &handler.Server{
		TokenMgr:       tokenMgr,
		Config:         cfg,
		GeneratedDir:   generatedDir,
		LeonardoClient: leoClient,
		ReqLog:         reqLogStore,
	}
	srv.StartTokenAutoRefreshLoop()

	mux := http.NewServeMux()

	// ─── OpenAI-compatible generation API ───
	mux.HandleFunc("/v1/models", srv.HandleListModels)
	mux.HandleFunc("/v1/images/generations", srv.HandleImageGeneration)
	mux.HandleFunc("/v1/chat/completions", srv.HandleChatCompletions)
	mux.HandleFunc("/v1/video/generations", srv.HandleVideoGeneration)
	mux.HandleFunc("/health", srv.HandleHealth)

	// ─── Admin auth ───
	mux.HandleFunc("/api/v1/auth/login", srv.HandleAuthLogin)
	mux.HandleFunc("/api/v1/auth/me", srv.HandleAuthMe)

	// ─── Token management (matches frontend /api/v1/tokens*) ───
	mux.HandleFunc("/api/v1/tokens/batch", srv.HandleTokenBatchAdd)
	mux.HandleFunc("/api/v1/tokens/delete-batch", srv.HandleDeleteBatch)
	mux.HandleFunc("/api/v1/tokens/status-batch", srv.HandleTokenStatusBatch)
	mux.HandleFunc("/api/v1/tokens/export", srv.HandleTokenExport)
	mux.HandleFunc("/api/v1/tokens/auto-refresh-batch", srv.HandleTokenAutoRefreshBatch)
	mux.HandleFunc("/api/v1/tokens/refresh-batch", srv.HandleTokenRefreshBatch)
	mux.HandleFunc("/api/v1/tokens/check-invalid-batch", srv.HandleCheckInvalidTokensBatch)

	// Token CRUD (must be after more specific /tokens/xxx routes)
	mux.HandleFunc("/api/v1/tokens/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/status") && (r.Method == "PUT" || r.Method == "PATCH"):
			srv.HandleTokenStatus(w, r)
		case strings.HasSuffix(path, "/refresh") && r.Method == "POST":
			srv.HandleTokenRefresh(w, r)
		case strings.HasSuffix(path, "/auto-refresh") && r.Method == "PUT":
			srv.HandleTokenAutoRefresh(w, r)
		case strings.Contains(path, "/refresh-jobs/"):
			if r.Method == "GET" {
				srv.HandleTokenRefreshJob(w, r)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		case r.Method == "DELETE":
			srv.HandleTokenDelete(w, r)
		default:
			http.Error(w, "not found", 404)
		}
	})
	mux.HandleFunc("/api/v1/tokens", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			srv.HandleTokenList(w, r)
		case "POST":
			srv.HandleTokenAdd(w, r)
		default:
			http.Error(w, "method not allowed", 405)
		}
	})

	// ─── Config ───
	mux.HandleFunc("/api/v1/config", func(w http.ResponseWriter, r *http.Request) {
		srv.HandleAdminConfig(w, r)
	})

	// ─── Logs ───
	mux.HandleFunc("/api/v1/logs/running", srv.HandleLogsRunning)
	mux.HandleFunc("/api/v1/logs/stats", srv.HandleLogsStats)
	mux.HandleFunc("/api/v1/logs", srv.HandleLogs)

	// ─── Refresh profiles ───
	mux.HandleFunc("/api/v1/refresh-profiles/import-cookie-batch", srv.HandleImportCookieBatch)
	mux.HandleFunc("/api/v1/refresh-profiles/export-cookies", srv.HandleCookieExport)
	mux.HandleFunc("/api/v1/refresh-profiles/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/import-cookie-jobs/") && r.Method == "GET":
			srv.HandleImportCookieJob(w, r)
		default:
			http.Error(w, "not found", 404)
		}
	})
	mux.HandleFunc("/api/v1/proxy/test", srv.HandleProxyTest)

	// ─── Leonardo API ───
	mux.HandleFunc("/api/v1/leonardo/validate", srv.HandleLeonardoValidate)
	mux.HandleFunc("/api/v1/leonardo/credits", srv.HandleLeonardoCredits)
	mux.HandleFunc("/api/v1/leonardo/generate", srv.HandleLeonardoGenerate)
	mux.HandleFunc("/api/v1/leonardo/status", srv.HandleLeonardoStatus)
	mux.HandleFunc("/api/v1/leonardo/upload-image", srv.HandleLeonardoUploadImage)

	// ─── Static files (admin UI) ───
	if info, statErr := os.Stat(staticDir); statErr == nil && info.IsDir() {
		fileServer := http.FileServer(http.Dir(staticDir))
		mux.Handle("/static/", http.StripPrefix("/static/", fileServer))
		mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, filepath.Join(staticDir, "login.html"))
		})
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" || r.URL.Path == "/admin" {
				http.ServeFile(w, r, filepath.Join(staticDir, "admin.html"))
				return
			}
			// Try serving static file
			filePath := filepath.Join(staticDir, r.URL.Path)
			if fi, err := os.Stat(filePath); err == nil && !fi.IsDir() {
				http.ServeFile(w, r, filePath)
				return
			}
			http.ServeFile(w, r, filepath.Join(staticDir, "admin.html"))
		})
	}

	// ─── Generated files ───
	mux.Handle("/generated/", http.StripPrefix("/generated/", http.FileServer(http.Dir(generatedDir))))

	h := corsMiddleware(loggingMiddleware(mux))

	addr := fmt.Sprintf("%s:%d", *host, *port)
	log.Printf("╔══════════════════════════════════════════╗")
	log.Printf("║       Leo-Go API Server v1.0.0           ║")
	log.Printf("╠══════════════════════════════════════════╣")
	log.Printf("║  Listening: http://%s", addr)
	log.Printf("║  Admin UI:  http://%s/", addr)
	log.Printf("║  Health:    http://%s/health", addr)
	log.Printf("║  Tokens:    %d loaded", tokenMgr.Count())
	log.Printf("╚══════════════════════════════════════════╝")

	if err := http.ListenAndServe(addr, h); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func seedRedisTokens(redisStore *store.RedisStore, sqliteStore *store.SQLiteStore) error {
	if redisStore == nil || sqliteStore == nil {
		return nil
	}
	redisTokens, err := redisStore.LoadTokens()
	if err != nil {
		return err
	}
	if len(redisTokens) > 0 {
		return nil
	}
	sqliteTokens, err := sqliteStore.LoadTokens()
	if err != nil {
		return err
	}
	if len(sqliteTokens) == 0 {
		return nil
	}
	if err := redisStore.ReplaceTokens(sqliteTokens); err != nil {
		return err
	}
	log.Printf("[redis] seeded %d tokens from sqlite", len(sqliteTokens))
	return nil
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !shouldSkipAccessLog(r.URL.Path) {
			log.Printf("[http] %s %s", r.Method, r.URL.Path)
		}
		next.ServeHTTP(w, r)
	})
}

func shouldSkipAccessLog(path string) bool {
	path = strings.TrimSpace(strings.ToLower(path))
	if path == "" {
		return false
	}
	if strings.HasPrefix(path, "/static/") || path == "/favicon.ico" {
		return true
	}
	if strings.HasPrefix(path, "/https:/fonts.googleapis.com") || strings.HasPrefix(path, "/https:/fonts.gstatic.com") {
		return true
	}

	noisyPaths := []string{
		"/console/",
		"/server",
		"/server-status",
		"/about",
		"/login.action",
		"/___proxy_subdomain_whm/login",
		"/___proxy_subdomain_cpanel",
		"/v2/_catalog",
		"/.ds_store",
	}
	for _, candidate := range noisyPaths {
		if path == candidate {
			return true
		}
	}
	return false
}
