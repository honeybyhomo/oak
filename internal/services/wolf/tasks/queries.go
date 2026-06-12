package tasks

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// getSinceHarvestQuery returns the total yield since the last harvest (or April 1).
// Prefers wolf_measurement (corrected daily yields from the daily API) and only
// falls back to SUM(hourly yield) for dates not yet in the measurement table.
func getSinceHarvestQuery(ctx context.Context, db *sql.DB, scaleUUID string, year int, loc *time.Location) (float64, error) {
	var lastHarvest sql.NullTime
	_ = db.QueryRowContext(ctx, `
		SELECT MAX(date) FROM wolf_harvest WHERE scale_id = $1
	`, scaleUUID).Scan(&lastHarvest)

	var since time.Time
	if lastHarvest.Valid {
		since = lastHarvest.Time
	} else {
		since = time.Date(year, 4, 1, 0, 0, 0, 0, loc)
	}

	sinceStr := since.Format("2006-01-02")

	// Prefer measurement (corrected yields from daily API)
	var measurementTotal sql.NullFloat64
	err := db.QueryRowContext(ctx, `
		SELECT SUM(yield) FROM wolf_measurement
		WHERE scale_id = $1 AND date > $2
	`, scaleUUID, sinceStr).Scan(&measurementTotal)
	if err != nil {
		return 0, err
	}

	// Fill gap: hourly yields for dates not covered by measurement
	var hourlyTotal sql.NullFloat64
	err = db.QueryRowContext(ctx, `
		SELECT SUM(yield) FROM wolf_hourly
		WHERE scale_id = $1 AND DATE(timestamp AT TIME ZONE 'Europe/Copenhagen') > $2
			AND DATE(timestamp AT TIME ZONE 'Europe/Copenhagen') NOT IN (
				SELECT date FROM wolf_measurement WHERE scale_id = $1
			)
	`, scaleUUID, sinceStr).Scan(&hourlyTotal)
	if err != nil {
		return 0, err
	}

	result := 0.0
	if measurementTotal.Valid {
		result += measurementTotal.Float64
	}
	if hourlyTotal.Valid {
		result += hourlyTotal.Float64
	}
	return result, nil
}

// getSeasonTotalQuery returns the total yield since April 1 of the given year.
// Same preference logic as getSinceHarvestQuery.
func getSeasonTotalQuery(ctx context.Context, db *sql.DB, scaleUUID string, year int, loc *time.Location) (float64, error) {
	seasonStart := time.Date(year, 4, 1, 0, 0, 0, 0, loc)
	sinceStr := seasonStart.Format("2006-01-02")

	var measurementTotal sql.NullFloat64
	err := db.QueryRowContext(ctx, `
		SELECT SUM(yield) FROM wolf_measurement
		WHERE scale_id = $1 AND date > $2
	`, scaleUUID, sinceStr).Scan(&measurementTotal)
	if err != nil {
		return 0, err
	}

	var hourlyTotal sql.NullFloat64
	err = db.QueryRowContext(ctx, `
		SELECT SUM(yield) FROM wolf_hourly
		WHERE scale_id = $1 AND DATE(timestamp AT TIME ZONE 'Europe/Copenhagen') > $2
			AND DATE(timestamp AT TIME ZONE 'Europe/Copenhagen') NOT IN (
				SELECT date FROM wolf_measurement WHERE scale_id = $1
			)
	`, scaleUUID, sinceStr).Scan(&hourlyTotal)
	if err != nil {
		return 0, err
	}

	result := 0.0
	if measurementTotal.Valid {
		result += measurementTotal.Float64
	}
	if hourlyTotal.Valid {
		result += hourlyTotal.Float64
	}
	return result, nil
}

// getDailyYieldQuery returns the total daily yield by summing hourly yields.
func getDailyYieldQuery(ctx context.Context, db *sql.DB, scaleUUID string, date time.Time) (float64, error) {
	var total sql.NullFloat64
	err := db.QueryRowContext(ctx, `
		SELECT SUM(yield) FROM wolf_hourly
		WHERE scale_id = $1 AND DATE(timestamp AT TIME ZONE 'Europe/Copenhagen') = $2
	`, scaleUUID, date.Format("2006-01-02")).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("no hourly data for %s: %w", date.Format("2006-01-02"), err)
	}
	if !total.Valid {
		return 0, fmt.Errorf("no hourly data for %s", date.Format("2006-01-02"))
	}
	return total.Float64, nil
}

// getWeightAtQuery returns the weight at 23:00 for the given date.
func getWeightAtQuery(ctx context.Context, db *sql.DB, scaleUUID string, date time.Time, loc *time.Location) (float64, error) {
	hour23 := time.Date(date.Year(), date.Month(), date.Day(), 23, 0, 0, 0, loc)
	var weight sql.NullFloat64
	err := db.QueryRowContext(ctx, `
		SELECT weight FROM wolf_hourly
		WHERE scale_id = $1 AND timestamp = $2
	`, scaleUUID, hour23.UTC()).Scan(&weight)
	if err != nil {
		return 0, fmt.Errorf("no weight for %s: %w", date.Format("2006-01-02"), err)
	}
	return weight.Float64, nil
}

// getLastInspectionQuery returns the date of the last checkup, formatted in Danish.
func getLastInspectionQuery(ctx context.Context, db *sql.DB, scaleUUID string) string {
	var lastDate sql.NullTime
	err := db.QueryRowContext(ctx, `
		SELECT date FROM wolf_checkup
		WHERE scale_id = $1
		ORDER BY date DESC
		LIMIT 1
	`, scaleUUID).Scan(&lastDate)
	if err != nil || !lastDate.Valid {
		return "Ingen"
	}
	return formatDanishDate(lastDate.Time)
}
