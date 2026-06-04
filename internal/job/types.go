package job

import (
	"context"
	"time"
)

// Task is a single unit of work within a job
type Task interface {
	Name() string
	ShouldRun() bool
	Run(ctx context.Context) (*Result, error)
}

// Result holds the outcome of a task execution
type Result struct {
	RecordsProcessed int
	DurationTotal    time.Duration
	DurationAPI      time.Duration
	DurationDB       time.Duration
}

// Definition describes a scheduled job with its tasks
type Definition struct {
	Kind           string
	Name           string
	Description    string
	Service        string
	Tasks          []Task
	Schedules      []string
	TimeoutMinutes int
	MaxAttempts    int
}
