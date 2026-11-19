// Reference Plat5 API: project-scoped tasks and user profiles behind Plat5's gateway.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// version is set at build time with -ldflags "-X main.version=$VERSION".
var version = "dev"

var permissionName = regexp.MustCompile(`^[a-z0-9:._-]{1,64}$`)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(); err != nil {
		slog.Error("api stopped", "error_message", err.Error())
		os.Exit(1)
	}
}

func run() error {
	assigneePermission := os.Getenv("TASK_ASSIGNEE_PERMISSION")
	if !permissionName.MatchString(assigneePermission) {
		return fmt.Errorf("TASK_ASSIGNEE_PERMISSION is %q: must match [a-z0-9:._-]{1,64} (e.g. project:read)", assigneePermission)
	}
	identityURL := os.Getenv("IDENTITY_INTERNAL_URL")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTelemetry, err := setupTelemetry(ctx, version)
	if err != nil {
		return err
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTelemetry(sctx); err != nil {
			slog.Error("telemetry shutdown failed", "error_message", err.Error())
		}
	}()

	sqlDB, err := openDB(ctx, env("DATABASE_PATH", "./data/app.db"))
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	app := newApp(
		newTaskHandler(newTaskStore(sqlDB), newAccessClient(identityURL), assigneePermission),
		newProfileHandler(newProfileStore(sqlDB)),
	)

	port, internalPort := env("PORT", "3000"), env("INTERNAL_PORT", "3001")
	internalSrv := &http.Server{
		Addr:              ":" + internalPort,
		Handler:           newInternalHandler(sqlDB),
		ReadHeaderTimeout: 5 * time.Second,
	}

	following := make(chan struct{})
	if identityURL != "" {
		go func() {
			defer close(following)
			newLifecycleConsumer(identityURL, sqlDB).run(ctx)
		}()
	} else {
		close(following)
		slog.Warn("IDENTITY_INTERNAL_URL is unset: lifecycle log disabled and assignee checks will return 503")
	}

	slog.Info("starting api server", "port", port, "internal_port", internalPort)

	errs := make(chan error, 2)
	go func() {
		if err := internalSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- fmt.Errorf("internal server: %w", err)
			return
		}
		errs <- nil
	}()
	go func() {
		errs <- app.Listen(":"+port, fiber.ListenConfig{
			GracefulContext:       ctx,
			DisableStartupMessage: true,
		})
	}()

	internalShutdown := make(chan error, 1)
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		internalShutdown <- internalSrv.Shutdown(sctx)
	}()

	err = <-errs
	stop()
	err = errors.Join(err, <-errs, <-internalShutdown)
	<-following
	return err
}

func newApp(tasks *taskHandler, profiles *profileHandler) *fiber.App {
	app := fiber.New(fiber.Config{
		ErrorHandler: errorHandler,
		// Body size limits belong to the Plat5 gateway (max_body_bytes).
		BodyLimit: math.MaxInt,
	})
	app.Use(telemetryMiddleware()...)
	app.Use(recover.New())

	app.Get("/users/:user_id/profile", profiles.get)
	app.Put("/users/:user_id/profile", profiles.put)

	app.Get("/projects/:project_id/tasks", tasks.list)
	app.Post("/projects/:project_id/tasks", tasks.create)
	app.Get("/projects/:project_id/tasks/:task_id", tasks.get)
	app.Patch("/projects/:project_id/tasks/:task_id", tasks.update)
	app.Delete("/projects/:project_id/tasks/:task_id", tasks.delete)
	return app
}

func newInternalHandler(sqlDB *sql.DB) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := sqlDB.PingContext(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
	})
	mux.Handle("GET /metrics", promhttp.Handler())
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
