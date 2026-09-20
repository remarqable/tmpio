// Command api is the tmp application server: one binary serving the browser
// UI, REST API, secret links, OAuth authorization server and MCP endpoint.
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/remarqable/tmpio/internal/assets"
	"github.com/remarqable/tmpio/internal/controllers"
	tmpmcp "github.com/remarqable/tmpio/internal/mcp"
	"github.com/remarqable/tmpio/internal/models"
	"github.com/remarqable/tmpio/internal/platform/ai"
	"github.com/remarqable/tmpio/internal/platform/auth"
	"github.com/remarqable/tmpio/internal/platform/config"
	"github.com/remarqable/tmpio/internal/platform/db"
	"github.com/remarqable/tmpio/internal/platform/i18n"
	"github.com/remarqable/tmpio/internal/platform/logger"
	"github.com/remarqable/tmpio/migrations"
)

func main() {
	// `tmpio healthcheck` asks the running server whether it is ready. The
	// container image has no shell and no curl, so the binary answers for both.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}

	logger.Init(os.Getenv("APP_ENV"))
	log := logger.Get()

	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("config")
	}
	if cfg.IsProd() {
		gin.SetMode(gin.ReleaseMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
		assets.SetDev(os.Getenv("TEMPLATE_DEV") == "1")
	}

	// With AUTO_MIGRATE the server brings the schema up to date itself, as the
	// owner role, before it opens its own restricted handle. That is what makes
	// a container update one command: pull the image and restart. Where an
	// operator runs `make migrate` by hand, leave it off.
	if cfg.AutoMigrate {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		// The runtime role first: its default privileges have to exist before
		// the migrations create the tables they apply to.
		if err := db.EnsureRuntimeRole(ctx, cfg.DatabaseOwnerURL, cfg.DatabaseURL); err != nil {
			cancel()
			log.Fatal().Err(err).Msg("runtime role")
		}
		version, err := db.MigrateUp(ctx, cfg.DatabaseOwnerURL, migrations.FS)
		cancel()
		if err != nil {
			log.Fatal().Err(err).Msg("migrate")
		}
		log.Info().Int64("version", version).Msg("schema up to date")
	}

	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("database")
	}
	db.SetDB(database)
	if sqlDB, err := database.DB(); err == nil {
		defer sqlDB.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := models.SyncOAuthClients(ctx, cfg.OAuthClients); err != nil {
		cancel()
		log.Fatal().Err(err).Msg("oauth clients (did you run migrations?)")
	}
	cancel()

	// The self-hosted owner account. Creating it here means a fresh instance is
	// usable the moment it boots, with no external identity provider.
	if cfg.LocalAuth {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		created, err := models.EnsureLocalOwner(ctx, cfg.OwnerUser, cfg.OwnerPassword, models.DefaultSiteConfigYAML, models.WelcomeMarkdown(cfg.AppOrigin))
		cancel()
		if err != nil {
			log.Fatal().Err(err).Msg("owner account")
		}
		log.Info().Str("user", cfg.OwnerUser).Bool("created", created).Msg("local owner account ready")
	}

	if err := i18n.Preload("en"); err != nil {
		log.Fatal().Err(err).Msg("i18n")
	}
	tmpl, err := assets.Templates(controllers.FuncMap())
	if err != nil {
		log.Fatal().Err(err).Msg("templates")
	}
	ops := &models.Ops{Quotas: cfg.Quotas, CursorKey: cfg.SessionSecret, AIMaxCallsPerHour: cfg.AI.MaxCallsPerHour}
	// The credential is resolved per call: the environment wins when it has
	// one, otherwise the instance settings page does, and either can change
	// while the server runs.
	resolver := ai.NewResolver(cfg.AI, func(ctx context.Context) (ai.Settings, error) {
		s, err := models.GetInstanceSetting(ctx)
		if err != nil {
			return ai.Settings{}, err
		}
		return ai.Settings{APIKey: s.AIAPIKey, Model: s.AIModel, BaseURL: s.AIBaseURL, WorkspaceID: s.AIWorkspaceID}, nil
	})
	ops.AI = resolver
	// ANTHROPIC_API_KEY seeds the stored setting once, so an instance
	// configured by environment keeps working and the settings page can still
	// change the key afterwards.
	if resolver.EnvConfigured() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		seeded, err := models.SeedInstanceAIFromEnv(ctx, models.InstanceAI{
			APIKey:      cfg.AI.APIKey,
			Model:       cfg.AI.Model,
			BaseURL:     cfg.AI.BaseURL,
			WorkspaceID: cfg.AI.WorkspaceID,
		})
		cancel()
		if err != nil {
			log.Warn().Err(err).Msg("ai filing: could not store the key from the environment")
		} else if seeded {
			log.Info().Msg("ai filing: stored the key from ANTHROPIC_API_KEY; change it in Server settings")
		}
	}
	log.Info().Bool("configured", resolver.Enabled(context.Background())).Msg("ai filing")
	deps := &controllers.Deps{Cfg: cfg, Ops: ops, Google: auth.NewGoogle(cfg), Tmpl: tmpl}
	mcpServer := tmpmcp.New(cfg, ops)

	srv := &http.Server{
		Addr:              "0.0.0.0:" + cfg.Port,
		Handler:           deps.SetupRouter(mcpServer.Handler()),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("listen")
		}
	}()
	log.Info().Str("addr", srv.Addr).Str("origin", cfg.AppOrigin).Bool("google", cfg.GoogleEnabled()).Bool("dev_bypass", cfg.DevLoginBypass).Msg("tmp listening")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	shutdown, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()
	_ = srv.Shutdown(shutdown)
}

// healthcheck returns 0 when the local server reports ready.
func healthcheck() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/readyz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
