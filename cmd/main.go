package main

import (
	"context"
	"fmt"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/honeybyhomo/oak/internal/config"
	"github.com/honeybyhomo/oak/internal/database"
	"github.com/honeybyhomo/oak/internal/job"
	"github.com/honeybyhomo/oak/internal/jobs"
	"github.com/honeybyhomo/oak/internal/logger"
	"github.com/honeybyhomo/oak/internal/scheduler"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Printf("Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	log := logger.New(&cfg.Logging)

	log.Info("Oak application starting",
		"environment", cfg.App.Environment,
		"port", cfg.App.Port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Connect to PostgreSQL (data + River queue)
	op := log.Track("Connect to PostgreSQL database")
	db, err := database.New(&cfg.Database)
	if err != nil {
		op.Error(err)
		fatalShutdown(log, nil, "Failed to connect to database", err)
	}
	op.Complete(1)

	log.Info("Database connection established",
		"host", cfg.Database.Host,
		"database", cfg.Database.Name)

	// Run migrations
	op = log.Track("Run database migrations")
	if err := database.RunMigrations(db.DB); err != nil {
		op.Error(err)
		fatalShutdown(log, db, "Failed to run migrations", err)
	}
	op.Complete(1)

	// Setup River (job queue) — uses same database
	schedClient, err := scheduler.NewClient(ctx, &cfg.Database, log)
	if err != nil {
		fatalShutdown(log, db, "Failed to create scheduler client", err)
	}

	op = log.Track("Run River migrations")
	if err := schedClient.Migrate(ctx); err != nil {
		op.Error(err)
		fatalShutdown(log, db, "Failed to run River migrations", err)
	}
	op.Complete(1)

	op = log.Track("Cleanup stuck jobs from previous run")
	cleanedCount, err := schedClient.CleanupStuckJobs(ctx)
	if err != nil {
		op.Error(err)
		log.Warn("Failed to cleanup stuck jobs", "error", err)
	} else {
		op.Complete(int(cleanedCount))
	}

	// Register jobs
	jobRegistry := job.NewRegistry()
	if err := jobs.RegisterAll(jobRegistry, db.DB, cfg, log); err != nil {
		fatalShutdown(log, db, "Failed to register jobs", err)
	}

	// Create scheduler with timezone-aware cron
	jobScheduler, err := job.NewScheduler(jobRegistry, job.SchedulerConfig{
		Timezone: cfg.Scheduler.Timezone,
	})
	if err != nil {
		fatalShutdown(log, db, "Failed to create job scheduler", err)
	}

	periodicJobs := jobScheduler.CreatePeriodicJobs()

	// Setup River worker
	workers := river.NewWorkers()
	river.AddWorker(workers, job.NewWorker(jobRegistry, db.DB, log))

	log.Info("Periodic jobs configured", "count", len(periodicJobs))

	riverClient, err := river.NewClient(riverpgxv5.New(schedClient.Pool()), &river.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: 5},
		},
		Workers:      workers,
		PeriodicJobs: periodicJobs,
		Logger:       log.SlogWarn(),
	})
	if err != nil {
		fatalShutdown(log, db, "Failed to create River client", err)
	}

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	if err := riverClient.Start(ctx); err != nil {
		fatalShutdown(log, db, "Failed to start River client", err)
	}

	log.Info("Scheduler started - press Ctrl+C to stop")

	// Health check endpoint
	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.App.Port),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	httpServer.Handler = mux

	go func() {
		log.Info("HTTP server starting", "address", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("HTTP server error", "error", err)
		}
	}()

	<-sigChan
	log.Info("Shutdown signal received")

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopCancel()

	if err := httpServer.Shutdown(stopCtx); err != nil {
		log.Error("Failed to shutdown HTTP server", "error", err)
	}

	if err := riverClient.StopAndCancel(stopCtx); err != nil {
		log.Error("Failed to stop River client", "error", err)
	}

	cancel()
	db.Close()
	schedClient.Pool().Close()
	log.Info("Oak application stopped gracefully")
}

func fatalShutdown(log *logger.Logger, db *database.DB, msg string, err error) {
	log.Error(msg, "error", err)
	if db != nil {
		db.Close()
	}
	os.Exit(1)
}
