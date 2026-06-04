package job

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/honeybyhomo/oak/internal/logger"
)

// JobArgs is the universal argument type for all Oak jobs
type JobArgs struct {
	JobKind string `json:"kind"`
}

func (a JobArgs) Kind() string {
	return "oak_job"
}

func (a JobArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateRunning,
				rivertype.JobStateScheduled,
				rivertype.JobStatePending,
				rivertype.JobStateAvailable,
				rivertype.JobStateRetryable,
			},
		},
	}
}

// Worker executes jobs from the River queue
type Worker struct {
	river.WorkerDefaults[JobArgs]
	registry *Registry
	db       *sql.DB
	logger   *logger.Logger
}

// NewWorker creates a new job worker
func NewWorker(registry *Registry, db *sql.DB, log *logger.Logger) *Worker {
	return &Worker{
		registry: registry,
		db:       db,
		logger:   log,
	}
}

// Work executes a single job from the River queue
func (w *Worker) Work(ctx context.Context, job *river.Job[JobArgs]) error {
	args := job.Args

	def, err := w.registry.Get(args.JobKind)
	if err != nil {
		return fmt.Errorf("job not found: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(def.TimeoutMinutes)*time.Minute)
	defer cancel()

	result, err := w.executeJob(ctx, def)

	var snoozeErr *rivertype.JobSnoozeError
	if errors.As(err, &snoozeErr) {
		w.logger.Warn("Job snoozed",
			"kind", args.JobKind,
			"duration", snoozeErr.Duration)
	} else {
		w.logger.Info("Job completed",
			"kind", args.JobKind,
			"records", result.RecordsProcessed,
			"duration", result.DurationTotal,
			"error", err)
	}

	return err
}

func (w *Worker) executeJob(ctx context.Context, def *Definition) (*Result, error) {
	totalResult := &Result{}
	var firstErr error

	for _, task := range def.Tasks {
		if !task.ShouldRun() {
			continue
		}

		result, err := task.Run(ctx)

		var snoozeErr *rivertype.JobSnoozeError
		isSnooze := errors.As(err, &snoozeErr)

		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if isSnooze {
				w.logger.Debug("Task snoozed", "task", task.Name(), "duration", snoozeErr.Duration)
			} else {
				w.logger.Error("Task failed", "task", task.Name(), "error", err)
			}
		}

		if result != nil {
			totalResult.RecordsProcessed += result.RecordsProcessed
			totalResult.DurationTotal += result.DurationTotal
			totalResult.DurationAPI += result.DurationAPI
			totalResult.DurationDB += result.DurationDB
		}
	}

	return totalResult, firstErr
}
