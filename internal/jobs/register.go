package jobs

import (
	"database/sql"

	"github.com/honeybyhomo/oak/internal/config"
	"github.com/honeybyhomo/oak/internal/job"
	"github.com/honeybyhomo/oak/internal/logger"
	"github.com/honeybyhomo/oak/internal/services/wolf"
	"github.com/honeybyhomo/oak/internal/services/wolf/tasks"
)

// RegisterAll registers all jobs with the registry
func RegisterAll(reg *job.Registry, db *sql.DB, cfg *config.Config, log *logger.Logger) error {
	wolfClient := wolf.New(&cfg.Services.Wolf, log)

	// Scales to fetch data for (read from config or hardcode for now)
	scales := []string{"G58E19"}

	// Wolf Daily — fetch and store yesterday's measurements
	if err := reg.Register(&job.Definition{
		Kind:        "wolf_daily",
		Name:        "Wolf Daily",
		Description: "Fetch and store daily measurements from Wolf Waagen",
		Service:     "wolf",
		Tasks: []job.Task{
			tasks.NewDailyMeasurementsTask(wolfClient, db, scales, log),
		},
		Schedules:      cfg.Jobs.WolfDaily.Schedules,
		TimeoutMinutes: cfg.Jobs.Global.TimeoutMinutes,
		MaxAttempts:    cfg.Jobs.Global.MaxAttempts,
	}); err != nil {
		return err
	}

	// Wolf Notify — send daily digest to Mattermost
	if err := reg.Register(&job.Definition{
		Kind:        "wolf_notify",
		Name:        "Wolf Notify",
		Description: "Send daily bee measurement digest to Mattermost",
		Service:     "wolf",
		Tasks: []job.Task{
			tasks.NewNotifyTask(db, &cfg.Jobs.Mattermost, scales, log),
		},
		Schedules:      cfg.Jobs.WolfNotify.Schedules,
		TimeoutMinutes: cfg.Jobs.Global.TimeoutMinutes,
		MaxAttempts:    cfg.Jobs.Global.MaxAttempts,
	}); err != nil {
		return err
	}

	// Wolf Backfill — fetch historical data (no schedule, manual only)
	if err := reg.Register(&job.Definition{
		Kind:        "wolf_backfill",
		Name:        "Wolf Backfill",
		Description: "Fetch and store historical measurements (7 days)",
		Service:     "wolf",
		Tasks: []job.Task{
			tasks.NewBackfillTask(wolfClient, db, scales, 7, log),
		},
		Schedules:      []string{}, // no automatic schedule
		TimeoutMinutes: cfg.Jobs.Global.TimeoutMinutes,
		MaxAttempts:    cfg.Jobs.Global.MaxAttempts,
	}); err != nil {
		return err
	}

	log.Info("All jobs registered", "count", len(reg.GetAll()))
	return nil
}
