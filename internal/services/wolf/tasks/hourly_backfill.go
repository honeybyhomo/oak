package tasks

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	"github.com/honeybyhomo/oak/internal/job"
	"github.com/honeybyhomo/oak/internal/logger"
	"github.com/honeybyhomo/oak/internal/services/wolf"
)

// HourlyBackfillTask fetches and stores historical hourly measurements.
// It fetches month-by-month from startYear to today to keep API requests manageable.
type HourlyBackfillTask struct {
	client    *wolf.Client
	db        *sql.DB
	scales    []string
	startYear int
	logger    *logger.Logger
}

// NewHourlyBackfillTask creates a new hourly backfill task
func NewHourlyBackfillTask(client *wolf.Client, db *sql.DB, scales []string, startYear int, log *logger.Logger) *HourlyBackfillTask {
	return &HourlyBackfillTask{
		client:    client,
		db:        db,
		scales:    scales,
		startYear: startYear,
		logger:    log,
	}
}

func (t *HourlyBackfillTask) Name() string { return "wolf_hourly_backfill" }

func (t *HourlyBackfillTask) ShouldRun() bool { return true }

func (t *HourlyBackfillTask) Run(ctx context.Context) (*job.Result, error) {
	start := time.Now()
	totalRecords := 0

	loc, err := time.LoadLocation("Europe/Copenhagen")
	if err != nil {
		return nil, fmt.Errorf("failed to load timezone: %w", err)
	}

	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	t.logger.Info("Starting hourly backfill",
		"from_year", t.startYear,
		"to", today.Format("2006-01-02"))

	// Fetch month by month to keep API responses manageable
	for year := t.startYear; year <= now.Year(); year++ {
		monthStart := time.January
		monthEnd := time.December

		for m := monthStart; m <= monthEnd; m++ {
			periodStart := time.Date(year, m, 1, 0, 0, 0, 0, loc)
			if periodStart.After(today) {
				break
			}

			// End of month
			periodEnd := time.Date(year, m+1, 0, 23, 0, 0, 0, loc)
			if periodEnd.After(today) {
				periodEnd = today.Add(-1 * time.Hour) // yesterday 23:00
			}

			for _, scaleID := range t.scales {
				records, err := t.backfillScaleMonth(ctx, scaleID, periodStart, periodEnd, loc)
				if err != nil {
					return nil, fmt.Errorf("failed to backfill %s %d-%02d scale %s: %w",
						time.Month(m).String(), year, m, scaleID, err)
				}
				totalRecords += records
			}
		}
	}

	return &job.Result{
		RecordsProcessed: totalRecords,
		DurationTotal:    time.Since(start),
	}, nil
}

func (t *HourlyBackfillTask) backfillScaleMonth(ctx context.Context, scaleID string, startDate, endDate time.Time, loc *time.Location) (int, error) {
	resp, err := t.client.FetchHourlyData(ctx, scaleID, startDate, endDate)
	if err != nil {
		return 0, err
	}

	var scaleUUID string
	err = t.db.QueryRowContext(ctx, `SELECT id FROM wolf_scale WHERE scale_id = $1`, scaleID).Scan(&scaleUUID)
	if err != nil {
		return 0, fmt.Errorf("scale %s not found in database: %w", scaleID, err)
	}

	weightSeries := wolf.FindSeries(resp, "weight")
	yieldSeries := wolf.FindSeries(resp, "yield")
	yieldSumSeries := wolf.FindSeries(resp, "yield_sum")
	tempSeries := wolf.FindSeries(resp, "temperature")

	if weightSeries == nil {
		return 0, nil // no data for this period
	}

	weights := weightSeries.FloatValues()
	yields := yieldSeries.FloatValues()
	yieldSums := yieldSumSeries.FloatValues()
	temps := tempSeries.FloatValues()

	pointStart := time.UnixMilli(resp.PointStart).In(loc)
	inserted := 0

	for i := 0; i < len(weights); i++ {
		ts := pointStart.Add(time.Duration(i) * time.Hour)

		if ts.Before(startDate) || ts.After(endDate) {
			continue
		}

		weight := weights[i]
		if math.IsNaN(weight) {
			continue
		}

		var yieldVal, yieldSumVal, tempVal sql.NullFloat64
		if i < len(yields) && !math.IsNaN(yields[i]) {
			yieldVal = sql.NullFloat64{Float64: yields[i], Valid: true}
		}
		if i < len(yieldSums) && !math.IsNaN(yieldSums[i]) {
			yieldSumVal = sql.NullFloat64{Float64: yieldSums[i], Valid: true}
		}
		if i < len(temps) && !math.IsNaN(temps[i]) {
			tempVal = sql.NullFloat64{Float64: temps[i], Valid: true}
		}

		_, err := t.db.ExecContext(ctx, `
			INSERT INTO wolf_hourly (scale_id, timestamp, weight, yield, yield_sum, temperature)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (scale_id, timestamp) DO UPDATE SET
				weight = EXCLUDED.weight,
				yield = EXCLUDED.yield,
				yield_sum = EXCLUDED.yield_sum,
				temperature = EXCLUDED.temperature,
				updated_at = NOW()
		`, scaleUUID, ts.UTC(), weight, yieldVal, yieldSumVal, tempVal)

		if err != nil {
			return inserted, fmt.Errorf("failed to insert hourly for %s: %w", ts.Format("2006-01-02 15:04"), err)
		}
		inserted++
	}

	// Store checkup items
	for _, checkup := range resp.CheckupItems {
		_, err := t.db.ExecContext(ctx, `
			INSERT INTO wolf_checkup (scale_id, external_id, date, time_begin, time_end, weight_begin, weight_end, checkup_type)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (scale_id, external_id) DO UPDATE SET
				time_begin = EXCLUDED.time_begin,
				time_end = EXCLUDED.time_end,
				weight_begin = EXCLUDED.weight_begin,
				weight_end = EXCLUDED.weight_end,
				checkup_type = EXCLUDED.checkup_type,
				updated_at = NOW()
		`, scaleUUID, checkup.ID, checkup.Date, checkup.TimeBegin, checkup.TimeEnd,
			checkup.WeightBegin, checkup.WeightEnd, checkup.Type)

		if err != nil {
			t.logger.Warn("Failed to insert checkup", "external_id", checkup.ID, "error", err)
		} else {
			inserted++
		}
	}

	if inserted > 0 {
		t.logger.Info("Hourly backfill chunk complete",
			"scale", scaleID,
			"period", startDate.Format("2006-01"),
			"records", inserted)
	}

	return inserted, nil
}
