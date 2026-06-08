package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/honeybyhomo/oak/internal/job"
	"github.com/honeybyhomo/oak/internal/logger"
	"github.com/honeybyhomo/oak/internal/services/wolf"
)

// BackfillTask fetches and stores historical measurements for a date range
type BackfillTask struct {
	client *wolf.Client
	db     *sql.DB
	scales []string
	days   int // number of days to backfill
	logger *logger.Logger
}

// NewBackfillTask creates a new backfill task
func NewBackfillTask(client *wolf.Client, db *sql.DB, scales []string, days int, log *logger.Logger) *BackfillTask {
	return &BackfillTask{
		client: client,
		db:     db,
		scales: scales,
		days:   days,
		logger: log,
	}
}

func (t *BackfillTask) Name() string { return "wolf_backfill" }

func (t *BackfillTask) ShouldRun() bool { return true }

func (t *BackfillTask) Run(ctx context.Context) (*job.Result, error) {
	start := time.Now()
	totalRecords := 0

	loc, err := time.LoadLocation("Europe/Copenhagen")
	if err != nil {
		return nil, fmt.Errorf("failed to load timezone: %w", err)
	}

	now := time.Now().In(loc)
	endDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	startDate := endDate.AddDate(0, 0, -t.days)

	t.logger.Info("Starting backfill",
		"from", startDate.Format("2006-01-02"),
		"to", endDate.Format("2006-01-02"),
		"days", t.days)

	for _, scaleID := range t.scales {
		records, err := t.backfillScale(ctx, scaleID, startDate, endDate)
		if err != nil {
			return nil, fmt.Errorf("failed to backfill scale %s: %w", scaleID, err)
		}
		totalRecords += records
	}

	return &job.Result{
		RecordsProcessed: totalRecords,
		DurationTotal:    time.Since(start),
	}, nil
}

func (t *BackfillTask) backfillScale(ctx context.Context, scaleID string, startDate, endDate time.Time) (int, error) {
	resp, err := t.client.FetchDailyData(ctx, scaleID, startDate, endDate)
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
	tempSeries := wolf.FindSeries(resp, "temperature")
	tempRangeSeries := wolf.FindSeries(resp, "temperature_range")

	if weightSeries == nil {
		return 0, fmt.Errorf("weight series not found in API response")
	}

	weights := weightSeries.FloatValues()
	yields := yieldSeries.FloatValues()
	temps := tempSeries.FloatValues()

	// Parse temp_range: each value is [min, max] pair
	type tempRangeVal struct {
		Min float64
		Max float64
	}
	var tempRanges []tempRangeVal
	if tempRangeSeries != nil {
		var rawArrays [][]float64
		if err := json.Unmarshal(tempRangeSeries.Values, &rawArrays); err == nil {
			for _, pair := range rawArrays {
				tr := tempRangeVal{}
				if len(pair) >= 2 {
					tr.Min = pair[0]
					tr.Max = pair[1]
				}
				tempRanges = append(tempRanges, tr)
			}
		}
	}

	pointStart := time.UnixMilli(resp.PointStart).In(startDate.Location())
	inserted := 0

	for i := 0; i < len(weights); i++ {
		date := pointStart.AddDate(0, 0, i)

		if date.Before(startDate) || !date.Before(endDate) {
			continue
		}

		weight := weights[i]
		if math.IsNaN(weight) {
			continue
		}

		var yieldVal sql.NullFloat64
		if i < len(yields) && !math.IsNaN(yields[i]) {
			yieldVal = sql.NullFloat64{Float64: yields[i], Valid: true}
		}

		var tempAvg sql.NullFloat64
		if i < len(temps) && !math.IsNaN(temps[i]) {
			tempAvg = sql.NullFloat64{Float64: temps[i], Valid: true}
		}

		var tempMin, tempMax sql.NullFloat64
		if i < len(tempRanges) {
			if !math.IsNaN(tempRanges[i].Min) {
				tempMin = sql.NullFloat64{Float64: tempRanges[i].Min, Valid: true}
			}
			if !math.IsNaN(tempRanges[i].Max) {
				tempMax = sql.NullFloat64{Float64: tempRanges[i].Max, Valid: true}
			}
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

	t.logger.Info("Backfill complete for scale",
		"scale", scaleID,
		"records", inserted)

	return inserted, nil
}
