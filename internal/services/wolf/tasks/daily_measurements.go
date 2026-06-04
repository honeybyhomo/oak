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

// DailyMeasurementsTask fetches and stores daily measurements from the Wolf API
type DailyMeasurementsTask struct {
	client *wolf.Client
	db     *sql.DB
	scales []string // scale IDs to fetch
	logger *logger.Logger
}

// NewDailyMeasurementsTask creates a new daily measurements task
func NewDailyMeasurementsTask(client *wolf.Client, db *sql.DB, scales []string, log *logger.Logger) *DailyMeasurementsTask {
	return &DailyMeasurementsTask{
		client: client,
		db:     db,
		scales: scales,
		logger: log,
	}
}

func (t *DailyMeasurementsTask) Name() string { return "wolf_daily_measurements" }

func (t *DailyMeasurementsTask) ShouldRun() bool { return true }

func (t *DailyMeasurementsTask) Run(ctx context.Context) (*job.Result, error) {
	start := time.Now()
	totalRecords := 0

	// Fetch yesterday's data (midnight to midnight Copenhagen time)
	loc, err := time.LoadLocation("Europe/Copenhagen")
	if err != nil {
		return nil, fmt.Errorf("failed to load timezone: %w", err)
	}

	now := time.Now().In(loc)
	yesterday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	today := yesterday.AddDate(0, 0, 1)

	for _, scaleID := range t.scales {
		records, err := t.fetchAndStore(ctx, scaleID, yesterday, today)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch measurements for scale %s: %w", scaleID, err)
		}
		totalRecords += records
	}

	return &job.Result{
		RecordsProcessed: totalRecords,
		DurationTotal:    time.Since(start),
		DurationAPI:      time.Since(start), // approximate — no separate API timing yet
	}, nil
}

func (t *DailyMeasurementsTask) fetchAndStore(ctx context.Context, scaleID string, start, end time.Time) (int, error) {
	apiStart := time.Now()
	resp, err := t.client.FetchDailyData(ctx, scaleID, start, end)
	apiDuration := time.Since(apiStart)
	if err != nil {
		return 0, err
	}

	// Get the scale's internal UUID
	var scaleUUID string
	err = t.db.QueryRowContext(ctx, `SELECT id FROM wolf_scale WHERE scale_id = $1`, scaleID).Scan(&scaleUUID)
	if err != nil {
		return 0, fmt.Errorf("scale %s not found in database: %w", scaleID, err)
	}

	// The API may include padding days (extend/pastExtend) before/after the requested range
	// pointStart is the first data point's timestamp
	pointStart := time.UnixMilli(resp.PointStart).In(start.Location())

	// Extract series
	weightSeries := wolf.FindSeries(resp, "weight")
	yieldSeries := wolf.FindSeries(resp, "yield")

	if weightSeries == nil {
		return 0, fmt.Errorf("weight series not found in API response")
	}

	// Use summary data for temperature (pre-aggregated)
	var tempMin, tempMax, tempAvg sql.NullFloat64
	tempSummary := wolf.FindSummary(resp, "temperature")
	if tempSummary != nil {
		for _, item := range tempSummary.Data {
			switch item.Type {
			case "avg":
				tempAvg = sql.NullFloat64{Float64: item.Value, Valid: true}
			case "min":
				tempMin = sql.NullFloat64{Float64: item.Value, Valid: true}
			case "max":
				tempMax = sql.NullFloat64{Float64: item.Value, Valid: true}
			}
		}
	}

	dbStart := time.Now()
	inserted := 0

	for i := 0; i < len(weightSeries.Values); i++ {
		date := pointStart.AddDate(0, 0, i)

		// Skip dates outside our requested range
		if date.Before(start) || !date.Before(end) {
			continue
		}

		weight := weightSeries.Values[i]
		if math.IsNaN(weight) {
			continue
		}

		var yieldVal sql.NullFloat64
		if yieldSeries != nil && i < len(yieldSeries.Values) && !math.IsNaN(yieldSeries.Values[i]) {
			yieldVal = sql.NullFloat64{Float64: yieldSeries.Values[i], Valid: true}
		}

		_, err := t.db.ExecContext(ctx, `
			INSERT INTO wolf_measurement (scale_id, date, weight, yield, temp_min, temp_max, temp_avg)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (scale_id, date) DO UPDATE SET
				weight = EXCLUDED.weight,
				yield = EXCLUDED.yield,
				temp_min = EXCLUDED.temp_min,
				temp_max = EXCLUDED.temp_max,
				temp_avg = EXCLUDED.temp_avg,
				updated_at = NOW()
		`, scaleUUID, date.Format("2006-01-02"), weight, yieldVal, tempMin, tempMax, tempAvg)

		if err != nil {
			return inserted, fmt.Errorf("failed to insert measurement for %s: %w", date.Format("2006-01-02"), err)
		}
		inserted++
	}

	// Store checkup items (scale on/off events)
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

	_ = apiDuration
	_ = dbStart
	_ = time.Since(dbStart)

	return inserted, nil
}
