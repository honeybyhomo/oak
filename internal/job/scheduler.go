package job

import (
	"github.com/riverqueue/river"
	"github.com/robfig/cron/v3"
)

// SchedulerConfig holds timezone for cron scheduling
type SchedulerConfig struct {
	Timezone string
}

// Scheduler creates periodic jobs from registered definitions
type Scheduler struct {
	registry *Registry
	config   SchedulerConfig
}

// NewScheduler creates a new job scheduler
func NewScheduler(registry *Registry, config SchedulerConfig) (*Scheduler, error) {
	return &Scheduler{
		registry: registry,
		config:   config,
	}, nil
}

// CreatePeriodicJobs converts all registered job schedules into River periodic jobs
func (s *Scheduler) CreatePeriodicJobs() []*river.PeriodicJob {
	var periodicJobs []*river.PeriodicJob

	for _, def := range s.registry.GetAll() {
		for _, scheduleStr := range def.Schedules {
			schedule, err := cron.ParseStandard(scheduleStr)
			if err != nil {
				continue
			}
			periodicJobs = append(periodicJobs, s.createPeriodicJob(def, schedule))
		}
	}

	return periodicJobs
}

func (s *Scheduler) createPeriodicJob(def *Definition, schedule cron.Schedule) *river.PeriodicJob {
	return river.NewPeriodicJob(
		schedule,
		func() (river.JobArgs, *river.InsertOpts) {
			return JobArgs{
					JobKind: def.Kind,
				}, &river.InsertOpts{
					MaxAttempts: def.MaxAttempts,
				}
		},
		&river.PeriodicJobOpts{RunOnStart: false},
	)
}
