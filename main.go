// The API: tasks inside a Plat5 project, and each person's profile, behind Plat5's gateway.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"
	"github.com/gofiber/fiber/v3/middleware/recover"

	"github.com/example/go-fiber-api/access"
	"github.com/example/go-fiber-api/db"
	"github.com/example/go-fiber-api/lifecycle"
	"github.com/example/go-fiber-api/plat5"
	"github.com/example/go-fiber-api/profiles"
	"github.com/example/go-fiber-api/tasks"
	"github.com/example/go-fiber-api/telemetry"
)

// version is the release this binary was built for, service.version, set with
// -ldflags "-X main.version=$VERSION". A build without one is dev.
var version = "dev"

// permissionName is a permission's name as Plat5's roles write it.
var permissionName = regexp.MustCompile(`^[a-z0-9:._-]{1,64}$`)

func main() {
	// Every line on stdout is JSON (plat5 docs/reference/telemetry.md#logs).
	slog.SetDefault(telemetry.NewLogger(os.Stdout))
	if err := run(); err != nil {
		slog.Error("api stopped", "error_message", err.Error())
		os.Exit(1)
	}
}

func run() error {
	// The permission an assignee needs on the task's project: the deployment's name for
	// it, never the code's.
	assigneePermission := os.Getenv("TASK_ASSIGNEE_PERMISSION")
	if !permissionName.MatchString(assigneePermission) {
		return fmt.Errorf("TASK_ASSIGNEE_PERMISSION is %q: set it to the permission an assignee needs on the task's project ([a-z0-9:._-], at most 64 characters; the starter's is project:read)", assigneePermission)
	}
	identityURL := os.Getenv("IDENTITY_INTERNAL_URL")

	// SIGINT or SIGTERM ends ctx: both servers shut down, and so does the lifecycle reader,
	// its read in flight cancelled.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdown, err := telemetry.Setup(ctx, version)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdown(ctx); err != nil {
			slog.Error("telemetry shutdown", "error_message", err.Error())
		}
	}()

	sqlDB, err := db.Open(ctx, env("DATABASE_PATH", "./data/app.db"))
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	taskStore, profileStore := tasks.NewStore(sqlDB), profiles.NewStore(sqlDB)
	app := newApp(tasks.NewHandler(taskStore, access.New(identityURL), assigneePermission), profiles.NewHandler(profileStore))

	internal := fiber.New(fiber.Config{ErrorHandler: plat5.ErrorHandler})
	internal.Get("/health/live", func(c fiber.Ctx) error { return c.JSON(fiber.Map{"status": "healthy"}) })
	internal.Get("/health/ready", func(c fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.Context(), 2*time.Second)
		defer cancel()
		if err := sqlDB.PingContext(ctx); err != nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"status": "unhealthy"})
		}
		return c.JSON(fiber.Map{"status": "healthy"})
	})
	internal.Get("/metrics", adaptor.HTTPHandler(telemetry.MetricsHandler()))

	following := make(chan struct{})
	if identityURL != "" {
		go func() {
			defer close(following)
			lifecycle.New(identityURL, sqlDB).Run(ctx)
		}()
	} else {
		close(following)
		slog.Warn("IDENTITY_INTERNAL_URL is unset: not reading identity's lifecycle log, so a purged project's tasks and a deleted person's profile are kept, and a task's assignee can't be checked (503)")
	}

	port, internalPort := env("PORT", "3000"), env("INTERNAL_PORT", "3001")
	slog.Info("starting api server", "port", port, "internal_port", internalPort)
	listen := fiber.ListenConfig{GracefulContext: ctx, DisableStartupMessage: true}
	errs := make(chan error, 2)
	go func() { errs <- internal.Listen(":"+internalPort, listen) }()
	go func() { errs <- app.Listen(":"+port, listen) }()
	err = <-errs
	stop()
	err = errors.Join(err, <-errs)
	// Wait for the reader, so its last transaction ends before the database closes.
	<-following
	return err
}

// newApp is the public API: its routes, behind the request telemetry and the error
// envelope.
func newApp(taskHandler *tasks.Handler, profileHandler *profiles.Handler) *fiber.App {
	app := fiber.New(fiber.Config{
		ErrorHandler: plat5.ErrorHandler,
		// A body's size is the gateway's to limit (max_body_bytes), not the API's.
		BodyLimit: math.MaxInt,
	})
	app.Use(telemetry.Middleware()...)
	app.Use(recover.New())

	app.Get("/users/:user_id/profile", profileHandler.Get)
	app.Put("/users/:user_id/profile", profileHandler.Put)

	// A project is a Plat5 node: its id is in the path, from the front door.
	app.Get("/projects/:project_id/tasks", taskHandler.List)
	app.Post("/projects/:project_id/tasks", taskHandler.Create)
	app.Get("/projects/:project_id/tasks/:task_id", taskHandler.Get)
	app.Patch("/projects/:project_id/tasks/:task_id", taskHandler.Update)
	app.Delete("/projects/:project_id/tasks/:task_id", taskHandler.Delete)
	return app
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
