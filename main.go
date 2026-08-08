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

	"github.com/example/go-fiber-api/db"
	"github.com/example/go-fiber-api/errors"
	"github.com/example/go-fiber-api/metrics"
	"github.com/example/go-fiber-api/middleware"
	"github.com/example/go-fiber-api/profiles"
	"github.com/example/go-fiber-api/projects"
	"github.com/example/go-fiber-api/tasks"
	"github.com/example/go-fiber-api/telemetry"
)

func main() {
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

	profileStore := profiles.NewStore(sqlDB)
	projectStore := projects.NewStore(sqlDB)
	taskStore := tasks.NewStore(sqlDB)

	profileHandler := profiles.NewHandler(profileStore, telem)
	projectHandler := projects.NewHandler(projectStore, telem)
	taskHandler := tasks.NewHandler(taskStore, projectStore, telem)

	app := fiber.New(fiber.Config{
		AppName:      "api",
		ErrorHandler: errors.FiberErrorHandler,
	})

	app.Use(recover.New())
	app.Use(otel.Middleware(
		otel.WithTracerProvider(telem.TracerProvider()),
		otel.WithPropagators(telem.Propagator()),
		otel.WithoutMetrics(true),
	))
	app.Use(middleware.RequestLogger(telem))

	profilesGroup := app.Group("/api/profiles", middleware.RequireUserID())
	profilesGroup.Get("/me", profileHandler.GetMe)
	profilesGroup.Put("/me", profileHandler.UpsertMe)
	profilesGroup.Get("/:user_id", profileHandler.GetByID)

	orgProjects := app.Group(
		"/api/organizations/:organization_id/projects",
		middleware.RequireOrg(),
	)
	orgProjects.Get("/", projectHandler.List)
	orgProjects.Post("/", projectHandler.Create)
	orgProjects.Get("/:project_id", projectHandler.Get)
	orgProjects.Patch("/:project_id", projectHandler.Update)
	orgProjects.Delete("/:project_id", projectHandler.Delete)

	orgTasks := app.Group(
		"/api/organizations/:organization_id/projects/:project_id/tasks",
		middleware.RequireOrg(),
	)
	orgTasks.Get("/", taskHandler.List)
	orgTasks.Post("/", taskHandler.Create)
	orgTasks.Get("/:task_id", taskHandler.Get)
	orgTasks.Patch("/:task_id", taskHandler.Update)
	orgTasks.Delete("/:task_id", taskHandler.Delete)

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

	baseLogger := telem.Logger()
	baseLogger.Info().
		Str("port", port).
		Str("internal_port", internalPort).
		Msg("starting api server")

	go func() {
		if err := internalApp.Listen(":" + internalPort); err != nil {
			baseLogger.Fatal().Err(err).Msg("internal server exited")
		}
	}()

	if err := app.Listen(":" + port); err != nil {
		baseLogger.Fatal().Err(err).Msg("fiber server exited")
	}
}
