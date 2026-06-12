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

	// Scales to fetch data for
	scales := []string{"G58E19"}

	// Wolf Hourly Sync — fetch hourly data and send notifications when ready
	if err := reg.Register(&job.Definition{
		Kind:        "wolf_hourly",
		Name:        "Wolf Hourly Sync",
		Description: "Fetch hourly measurements from Wolf Waagen and send notifications when data is complete",
		Service:     "wolf",
		Tasks: []job.Task{
			tasks.NewHourlySyncTask(wolfClient, db, &cfg.Jobs.Mattermost, scales, log),
		},
		Schedules:      cfg.Jobs.WolfHourly.Schedules,
		TimeoutMinutes: cfg.Jobs.Global.TimeoutMinutes,
		MaxAttempts:    cfg.Jobs.Global.MaxAttempts,
	}); err != nil {
		return err
	}

	// Wolf Backfill — fetch historical daily data (no schedule, manual only)
	// Fetches year by year from 2016 (scale's first year) to today
	if err := reg.Register(&job.Definition{
		Kind:        "wolf_backfill",
		Name:        "Wolf Backfill",
		Description: "Fetch and store all historical daily measurements (2016-present)",
		Service:     "wolf",
		Tasks: []job.Task{
			tasks.NewBackfillTask(wolfClient, db, scales, 2016, log),
		},
		Schedules:      []string{}, // no automatic schedule
		TimeoutMinutes: cfg.Jobs.Global.TimeoutMinutes,
		MaxAttempts:    cfg.Jobs.Global.MaxAttempts,
	}); err != nil {
		return err
	}

	// Wolf Hourly Backfill — fetch historical hourly data (no schedule, manual only)
	// Fetches month by month from 2023 (Valby scale start) to today
	if err := reg.Register(&job.Definition{
		Kind:        "wolf_hourly_backfill",
		Name:        "Wolf Hourly Backfill",
		Description: "Fetch and store all historical hourly measurements (2023-present)",
		Service:     "wolf",
		Tasks: []job.Task{
			tasks.NewHourlyBackfillTask(wolfClient, db, scales, 2023, log),
		},
		Schedules:      []string{}, // no automatic schedule
		TimeoutMinutes: 30,         // longer timeout for bulk fetch
		MaxAttempts:    cfg.Jobs.Global.MaxAttempts,
	}); err != nil {
		return err
	}

	log.Info("All jobs registered", "count", len(reg.GetAll()))
	return nil
}
