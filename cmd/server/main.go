package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"procurementcore/internal/api"
	"procurementcore/internal/auth"
	"procurementcore/internal/config"
	"procurementcore/internal/database"
	"procurementcore/internal/scraper"

	commonbranding "github.com/nbt4/cores-common/pkg/branding"
	"github.com/rs/zerolog"
)

const version = "1.0.18"

//go:embed all:dist
var frontend embed.FS

func main() {
	log := zerolog.New(os.Stderr).With().Timestamp().Str("service", "procurementcore").Logger()
	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("invalid configuration")
	}
	db, err := database.Open(cfg.DatabaseDSN)
	if err != nil {
		log.Fatal().Err(err).Msg("database startup failed")
	}
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatal().Err(err).Msg("database handle failed")
	}

	branding := commonbranding.NewService(db, "procurement")
	dist, err := fs.Sub(frontend, "dist")
	if err != nil {
		log.Fatal().Err(err).Msg("frontend unavailable")
	}
	assets := http.FileServer(http.FS(dist))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		status := http.StatusOK
		state := "ok"
		if err := sqlDB.PingContext(ctx); err != nil {
			status, state = http.StatusServiceUnavailable, "error"
		}
		writeJSON(w, status, map[string]string{"status": state, "service": "procurementcore", "version": version})
	})
	mux.HandleFunc("GET /api/v1/branding", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, branding.GetConfig()) })
	mux.HandleFunc("POST /api/v1/auth/logout", logoutHandler(cfg.CookieDomain))
	mux.HandleFunc("GET /logos/{filename}", func(w http.ResponseWriter, r *http.Request) {
		filename := filepath.Base(r.PathValue("filename"))
		if filename == "." || filename == "" || filename != r.PathValue("filename") {
			http.NotFound(w, r)
			return
		}
		volumePath := filepath.Join("/var/lib/branding/logos", filename)
		if info, statErr := os.Stat(volumePath); statErr == nil && !info.IsDir() {
			http.ServeFile(w, r, volumePath)
			return
		}
		data, readErr := fs.ReadFile(dist, "logos/"+filename)
		if readErr != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(filename)))
		_, _ = w.Write(data)
	})
	mux.HandleFunc("GET /manifest.webmanifest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/manifest+json")
		w.Header().Set("Cache-Control", "no-cache")
		_ = json.NewEncoder(w).Encode(commonbranding.Manifest(branding.GetConfig(), commonbranding.ManifestOptions{
			Name: "ProcurementCore", StartURL: "/", Scope: "/", ThemeColor: "#101719", BackgroundColor: "#101719",
			FallbackIcon192: "/app-icons/icon-192.png", FallbackIcon512: "/app-icons/icon-512.png",
			FallbackMaskable: "/app-icons/icon-maskable-512.png",
		}))
	})
	productScraper := scraper.New(scraper.Options{
		AdamHallUsername: cfg.AdamHallUsername,
		AdamHallPassword: cfg.AdamHallPassword,
	})
	mux.Handle("/api/v1/", http.StripPrefix("/api/v1", auth.Middleware(api.NewHandler(db, productScraper).Routes())))

	mux.Handle("GET /assets/", assets)
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		log.Fatal().Err(err).Msg("index unavailable")
	}
	index = []byte(strings.Replace(string(index), "</head>", fmt.Sprintf("<script>window.__DASHBOARD_URL__=%q</script></head>", cfg.DashboardURL), 1))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, ".") {
			assets.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})

	handler := securityHeaders(requestLog(log, recoverer(mux)))
	server := &http.Server{Addr: ":" + cfg.Port, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second}
	go func() {
		log.Info().Str("port", cfg.Port).Str("version", version).Msg("started")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("server stopped")
		}
	}()
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
	_ = sqlDB.Close()
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Interner Serverfehler"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
func requestLog(log zerolog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Info().Str("method", r.Method).Str("path", r.URL.Path).Dur("duration", time.Since(start)).Msg("request")
	})
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}
func logoutHandler(cookieDomain string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     "cores_token",
			Value:    "",
			Path:     "/",
			Domain:   cookieDomain,
			HttpOnly: true,
			Secure:   cookieDomain != "",
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
		})
		writeJSON(w, http.StatusOK, map[string]bool{"success": true})
	}
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
