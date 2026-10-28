package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/gofiber/contrib/v3/otel"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"
	"github.com/gofiber/fiber/v3/middleware/recover"

	"github.com/example/go-fiber-api/access"
	"github.com/example/go-fiber-api/db"
	"github.com/example/go-fiber-api/errors"
	"github.com/example/go-fiber-api/lifecycle"
	"github.com/example/go-fiber-api/metrics"
	"github.com/example/go-fiber-api/middleware"
	"github.com/example/go-fiber-api/profiles"
	"github.com/example/go-fiber-api/tasks"
	"github.com/example/go-fiber-api/telemetry"
)

func main() {
	// The permission an assignee needs on the task's project: the deployment's name for it,
	// never the code's.
	assigneePermission := os.Getenv("TASK_ASSIGNEE_PERMISSION")
	if assigneePermission == "" {
		log.Fatal("TASK_ASSIGNEE_PERMISSION is unset or empty: set it to the permission an assignee needs on the task's project (the starter's is project:read)")
	}
	identityURL := os.Getenv("IDENTITY_INTERNAL_URL")

	ctx := context.Background()
	metrics.Init()

	telem, err := telemetry.Init(ctx)
	if err != nil {
		log.Fatalf("failed to initialize telemetry: %v", err)
	}
	defer func() {
		if err := telem.Shutdown(context.Background()); err != nil {
			log.Printf("error shutting down telemetry: %v", err)
		}
	}()

	sqlDB, err := db.Connect(ctx)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer sqlDB.Close()

	if err := db.Migrate(ctx, sqlDB); err != nil {
		log.Fatalf("failed to migrate database: %v", err)
	}

	baseLogger := telem.Logger()

	profileStore := profiles.NewStore(sqlDB)
	taskStore := tasks.NewStore(sqlDB)

	profileHandler := profiles.NewHandler(profileStore, telem)
	taskHandler := tasks.NewHandler(taskStore, telem, access.New(identityURL, telem.Propagator(), baseLogger), assigneePermission)

	app := fiber.New(fiber.Config{
		AppName:      "api",
		BodyLimit:    errors.MaxBodyBytes,
		ErrorHandler: errors.FiberErrorHandler,
	})

	app.Use(recover.New())
	app.Use(otel.Middleware(
		otel.WithTracerProvider(telem.TracerProvider()),
		otel.WithPropagators(telem.Propagator()),
		otel.WithoutMetrics(true),
		otel.WithSpanNameFormatter(middleware.HTTPSpanName),
	))
	app.Use(middleware.RequestLogger(telem))

	app.Get("/users/:user_id/profile", profileHandler.Get)
	app.Put("/users/:user_id/profile", profileHandler.Upsert)

	// An environment is a Plat5 node: its id is in the path, from the front door.
	taskRoutes := app.Group("/environments/:environment_id/tasks")
	taskRoutes.Get("/", taskHandler.List)
	taskRoutes.Post("/", taskHandler.Create)
	taskRoutes.Get("/:task_id", taskHandler.Get)
	taskRoutes.Patch("/:task_id", taskHandler.Update)
	taskRoutes.Delete("/:task_id", taskHandler.Delete)

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	internalApp := fiber.New(fiber.Config{
		AppName:      "api-internal",
		ErrorHandler: errors.FiberErrorHandler,
	})
	internalApp.Get("/health/live", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "healthy"})
	})
	internalApp.Get("/health/ready", func(c fiber.Ctx) error {
		pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := sqlDB.PingContext(pingCtx); err != nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"status": "unhealthy"})
		}
		return c.JSON(fiber.Map{"status": "healthy"})
	})
	internalApp.Get("/metrics", adaptor.HTTPHandler(metrics.Handler()))

	internalPort := os.Getenv("INTERNAL_PORT")
	if internalPort == "" {
		internalPort = "3001"
	}

	baseLogger.Info().
		Str("port", port).
		Str("internal_port", internalPort).
		Msg("starting api server")

	if identityURL != "" {
		go lifecycle.New(identityURL, sqlDB, taskStore, profileStore, baseLogger).Run(ctx)
	} else {
		baseLogger.Warn().Msg("IDENTITY_INTERNAL_URL is unset: not reading identity's feeds, so a purged environment's tasks and a deleted person's profile are kept, and a task's assignee can't be checked (503)")
	}

	go func() {
		if err := internalApp.Listen(":" + internalPort); err != nil {
			baseLogger.Fatal().Err(err).Msg("internal server exited")
		}
	}()

	if err := app.Listen(":" + port); err != nil {
		baseLogger.Fatal().Err(err).Msg("fiber server exited")
	}
}
