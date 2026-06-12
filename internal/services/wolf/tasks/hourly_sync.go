package tasks

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/honeybyhomo/oak/internal/config"
	"github.com/honeybyhomo/oak/internal/job"
	"github.com/honeybyhomo/oak/internal/logger"
	"github.com/honeybyhomo/oak/internal/services/wolf"
)

// HourlySyncTask fetches hourly data from the Wolf API, stores it,
// and sends notifications when yesterday's data is complete.
type HourlySyncTask struct {
	client *wolf.Client
	db     *sql.DB
	config *config.MattermostConfig
	scales []string
	logger *logger.Logger
}

// NewHourlySyncTask creates a new hourly sync task
func NewHourlySyncTask(client *wolf.Client, db *sql.DB, cfg *config.MattermostConfig, scales []string, log *logger.Logger) *HourlySyncTask {
	return &HourlySyncTask{
		client: client,
		db:     db,
		config: cfg,
		scales: scales,
		logger: log,
	}
}

func (t *HourlySyncTask) Name() string { return "wolf_hourly_sync" }

func (t *HourlySyncTask) ShouldRun() bool { return true }

func (t *HourlySyncTask) Run(ctx context.Context) (*job.Result, error) {
	start := time.Now()
	totalRecords := 0

	loc, err := time.LoadLocation("Europe/Copenhagen")
	if err != nil {
		return nil, fmt.Errorf("failed to load timezone: %w", err)
	}

	now := time.Now().In(loc)

	for _, scaleID := range t.scales {
		// Determine fetch window: from last stored hourly entry to now
		fetchStart, err := t.getLastHourlyTimestamp(ctx, scaleID)
		if err != nil {
			return nil, fmt.Errorf("failed to get last hourly timestamp: %w", err)
		}
		fetchEnd := now

		// Fetch and store hourly data
		records, err := t.fetchAndStore(ctx, scaleID, fetchStart, fetchEnd, loc)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch hourly data for scale %s: %w", scaleID, err)
		}
		totalRecords += records

		// Try to send notifications
		notifCount, err := t.trySendNotifications(ctx, scaleID, now, loc)
		if err != nil {
			t.logger.Error("Failed to send notifications", "scale", scaleID, "error", err)
		}
		totalRecords += notifCount
	}

	return &job.Result{
		RecordsProcessed: totalRecords,
		DurationTotal:    time.Since(start),
	}, nil
}

// getLastHourlyTimestamp returns the timestamp to start fetching from.
// It looks for the most recent hourly entry and goes back 3 days as a safety margin,
// or defaults to 7 days ago if no data exists.
func (t *HourlySyncTask) getLastHourlyTimestamp(ctx context.Context, scaleID string) (time.Time, error) {
	var scaleUUID string
	err := t.db.QueryRowContext(ctx, `SELECT id FROM wolf_scale WHERE scale_id = $1`, scaleID).Scan(&scaleUUID)
	if err != nil {
		return time.Time{}, fmt.Errorf("scale %s not found: %w", scaleID, err)
	}

	var lastTs sql.NullTime
	err = t.db.QueryRowContext(ctx, `
		SELECT MAX(timestamp) FROM wolf_hourly WHERE scale_id = $1
	`, scaleUUID).Scan(&lastTs)
	if err != nil {
		return time.Time{}, err
	}

	if lastTs.Valid {
		// Go back 3 days from last entry to catch any gaps
		return lastTs.Time.AddDate(0, 0, -3), nil
	}

	// No data yet — default to 7 days ago
	return time.Now().AddDate(0, 0, -7), nil
}

func (t *HourlySyncTask) fetchAndStore(ctx context.Context, scaleID string, fetchStart, fetchEnd time.Time, loc *time.Location) (int, error) {
	resp, err := t.client.FetchHourlyData(ctx, scaleID, fetchStart, fetchEnd)
	if err != nil {
		return 0, err
	}

	var scaleUUID string
	err = t.db.QueryRowContext(ctx, `SELECT id FROM wolf_scale WHERE scale_id = $1`, scaleID).Scan(&scaleUUID)
	if err != nil {
		return 0, fmt.Errorf("scale %s not found: %w", scaleID, err)
	}

	weightSeries := wolf.FindSeries(resp, "weight")
	yieldSeries := wolf.FindSeries(resp, "yield")
	yieldSumSeries := wolf.FindSeries(resp, "yield_sum")
	tempSeries := wolf.FindSeries(resp, "temperature")

	if weightSeries == nil {
		return 0, nil // no data
	}

	weights := weightSeries.FloatValues()
	yields := yieldSeries.FloatValues()
	yieldSums := yieldSumSeries.FloatValues()
	temps := tempSeries.FloatValues()

	pointStart := time.UnixMilli(resp.PointStart).In(loc)
	inserted := 0

	for i := 0; i < len(weights); i++ {
		ts := pointStart.Add(time.Duration(i) * time.Hour)

		// Skip entries outside our fetch window
		if ts.Before(fetchStart) || ts.After(fetchEnd) {
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
		}
	}

	if inserted > 0 {
		t.logger.Info("Stored hourly data", "scale", scaleID, "records", inserted)
	}

	return inserted, nil
}

// trySendNotifications checks if notifications should be sent and sends them
func (t *HourlySyncTask) trySendNotifications(ctx context.Context, scaleID string, now time.Time, loc *time.Location) (int, error) {
	scaleUUID, err := t.getScaleUUID(ctx, scaleID)
	if err != nil {
		return 0, err
	}

	yesterday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -1)
	sent := 0

	// Check if daily notification for yesterday has been sent
	dailySent, err := t.isNotificationSent(ctx, scaleUUID, yesterday, "daily")
	if err != nil {
		return 0, err
	}
	if dailySent {
		return 0, nil // already sent
	}

	// Check if we have the 23:00 reading for yesterday (complete day)
	hasCompleteDay, err := t.hasCompleteDay(ctx, scaleUUID, yesterday, loc)
	if err != nil {
		return 0, err
	}
	if !hasCompleteDay {
		t.logger.Debug("Yesterday's data not yet complete, skipping notification")
		return 0, nil
	}

	// Send daily notification
	msg, err := t.buildDailyMessage(ctx, scaleUUID, yesterday, loc)
	if err != nil {
		return 0, fmt.Errorf("failed to build daily message: %w", err)
	}
	if err := t.sendToMattermost(ctx, msg); err != nil {
		return 0, fmt.Errorf("failed to send daily notification: %w", err)
	}
	t.logNotification(ctx, scaleUUID, yesterday, "daily")
	sent++

	// Check if we should also send weekly (Monday = yesterday was Sunday)
	isWeekly := now.Weekday() == time.Monday
	if isWeekly {
		weeklySent, err := t.isNotificationSent(ctx, scaleUUID, yesterday, "weekly")
		if err != nil {
			return sent, err
		}
		if !weeklySent {
			weekStart := yesterday.AddDate(0, 0, -6) // Monday
			msg, err := t.buildWeeklyMessage(ctx, scaleUUID, weekStart, yesterday, loc)
			if err != nil {
				return sent, fmt.Errorf("failed to build weekly message: %w", err)
			}
			if err := t.sendToMattermost(ctx, msg); err != nil {
				return sent, fmt.Errorf("failed to send weekly notification: %w", err)
			}
			t.logNotification(ctx, scaleUUID, yesterday, "weekly")
			sent++
		}
	}

	return sent, nil
}

// hasCompleteDay checks if we have the 23:00 reading for the given date
func (t *HourlySyncTask) hasCompleteDay(ctx context.Context, scaleUUID string, date time.Time, loc *time.Location) (bool, error) {
	// We need a yield_sum entry at 23:00 for that date
	hour23 := time.Date(date.Year(), date.Month(), date.Day(), 23, 0, 0, 0, loc)
	var count int
	err := t.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM wolf_hourly
		WHERE scale_id = $1 AND timestamp = $2 AND yield_sum IS NOT NULL
	`, scaleUUID, hour23.UTC()).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// isNotificationSent checks if a notification has already been sent
func (t *HourlySyncTask) isNotificationSent(ctx context.Context, scaleUUID string, date time.Time, notifType string) (bool, error) {
	var count int
	err := t.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM wolf_notification_log
		WHERE scale_id = $1 AND date = $2 AND notification_type = $3
	`, scaleUUID, date.Format("2006-01-02"), notifType).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// logNotification records that a notification was sent
func (t *HourlySyncTask) logNotification(ctx context.Context, scaleUUID string, date time.Time, notifType string) {
	_, err := t.db.ExecContext(ctx, `
		INSERT INTO wolf_notification_log (scale_id, date, notification_type)
		VALUES ($1, $2, $3)
		ON CONFLICT (scale_id, date, notification_type) DO NOTHING
	`, scaleUUID, date.Format("2006-01-02"), notifType)
	if err != nil {
		t.logger.Warn("Failed to log notification", "error", err)
	}
}

// getScaleUUID returns the internal UUID for a scale
func (t *HourlySyncTask) getScaleUUID(ctx context.Context, scaleID string) (string, error) {
	var uuid string
	err := t.db.QueryRowContext(ctx, `SELECT id FROM wolf_scale WHERE scale_id = $1`, scaleID).Scan(&uuid)
	if err != nil {
		return "", fmt.Errorf("scale %s not found: %w", scaleID, err)
	}
	return uuid, nil
}

// getDailyYield returns the total daily yield for the given date by summing hourly yields.
// The API's yield_sum field is cumulative (relative to the fetch window) and cannot be
// used as a daily yield value.
func (t *HourlySyncTask) getDailyYield(ctx context.Context, scaleUUID string, date time.Time, loc *time.Location) (float64, error) {
	var total sql.NullFloat64
	err := t.db.QueryRowContext(ctx, `
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

// getSinceHarvest returns the total yield since the last harvest (or April 1).
// It combines data from wolf_hourly (for dates with hourly data) and wolf_measurement
// (for historical dates with daily data only), using SUM(yield) from hourly data
// where available since the API's yield_sum is cumulative relative to the fetch window.
func (t *HourlySyncTask) getSinceHarvest(ctx context.Context, scaleUUID string, year int, loc *time.Location) (float64, error) {
	// Check for last harvest
	var lastHarvest sql.NullTime
	_ = t.db.QueryRowContext(ctx, `
		SELECT MAX(date) FROM wolf_harvest WHERE scale_id = $1
	`, scaleUUID).Scan(&lastHarvest)

	var since time.Time
	if lastHarvest.Valid {
		since = lastHarvest.Time
	} else {
		since = time.Date(year, 4, 1, 0, 0, 0, 0, loc)
	}

	sinceStr := since.Format("2006-01-02")

	// Get hourly-based yields
	var hourlyTotal sql.NullFloat64
	err := t.db.QueryRowContext(ctx, `
		SELECT SUM(yield) FROM wolf_hourly
		WHERE scale_id = $1 AND DATE(timestamp AT TIME ZONE 'Europe/Copenhagen') > $2
	`, scaleUUID, sinceStr).Scan(&hourlyTotal)
	if err != nil {
		return 0, err
	}

	// Get measurement-based yields for dates NOT covered by hourly data
	var measurementTotal sql.NullFloat64
	err = t.db.QueryRowContext(ctx, `
		SELECT SUM(yield) FROM wolf_measurement
		WHERE scale_id = $1 AND date > $2
			AND date NOT IN (
				SELECT DISTINCT DATE(timestamp AT TIME ZONE 'Europe/Copenhagen')
				FROM wolf_hourly WHERE scale_id = $1
			)
	`, scaleUUID, sinceStr).Scan(&measurementTotal)
	if err != nil {
		return 0, err
	}

	result := 0.0
	if hourlyTotal.Valid {
		result += hourlyTotal.Float64
	}
	if measurementTotal.Valid {
		result += measurementTotal.Float64
	}
	return result, nil
}

// getWeightAt returns the weight at 23:00 for the given date
func (t *HourlySyncTask) getWeightAt(ctx context.Context, scaleUUID string, date time.Time, loc *time.Location) (float64, error) {
	hour23 := time.Date(date.Year(), date.Month(), date.Day(), 23, 0, 0, 0, loc)
	var weight sql.NullFloat64
	err := t.db.QueryRowContext(ctx, `
		SELECT weight FROM wolf_hourly
		WHERE scale_id = $1 AND timestamp = $2
	`, scaleUUID, hour23.UTC()).Scan(&weight)
	if err != nil {
		return 0, fmt.Errorf("no weight for %s: %w", date.Format("2006-01-02"), err)
	}
	return weight.Float64, nil
}

// getLastInspection returns the date of the last checkup, formatted in Danish
func (t *HourlySyncTask) getLastInspection(ctx context.Context, scaleUUID string) string {
	var lastDate sql.NullTime
	err := t.db.QueryRowContext(ctx, `
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

// getSeasonTotal returns the total yield since April 1 of the given year.
// It combines data from wolf_hourly and wolf_measurement to cover the full season.
func (t *HourlySyncTask) getSeasonTotal(ctx context.Context, scaleUUID string, year int, loc *time.Location) (float64, error) {
	seasonStart := time.Date(year, 4, 1, 0, 0, 0, 0, loc)
	sinceStr := seasonStart.Format("2006-01-02")

	var hourlyTotal sql.NullFloat64
	err := t.db.QueryRowContext(ctx, `
		SELECT SUM(yield) FROM wolf_hourly
		WHERE scale_id = $1 AND DATE(timestamp AT TIME ZONE 'Europe/Copenhagen') > $2
	`, scaleUUID, sinceStr).Scan(&hourlyTotal)
	if err != nil {
		return 0, err
	}

	var measurementTotal sql.NullFloat64
	err = t.db.QueryRowContext(ctx, `
		SELECT SUM(yield) FROM wolf_measurement
		WHERE scale_id = $1 AND date > $2
			AND date NOT IN (
				SELECT DISTINCT DATE(timestamp AT TIME ZONE 'Europe/Copenhagen')
				FROM wolf_hourly WHERE scale_id = $1
			)
	`, scaleUUID, sinceStr).Scan(&measurementTotal)
	if err != nil {
		return 0, err
	}

	result := 0.0
	if hourlyTotal.Valid {
		result += hourlyTotal.Float64
	}
	if measurementTotal.Valid {
		result += measurementTotal.Float64
	}
	return result, nil
}

// buildDailyMessage creates the daily one-liner notification
func (t *HourlySyncTask) buildDailyMessage(ctx context.Context, scaleUUID string, date time.Time, loc *time.Location) (string, error) {
	dailyYield, err := t.getDailyYield(ctx, scaleUUID, date, loc)
	if err != nil {
		return "", err
	}

	sinceHarvest, err := t.getSinceHarvest(ctx, scaleUUID, date.Year(), loc)
	if err != nil {
		return "", fmt.Errorf("failed to get since-harvest: %w", err)
	}

	yieldStr := formatYield(dailyYield)
	sinceStr := formatKg(sinceHarvest)
	dayAbbr := danishWeekdays[date.Weekday()]
	dateStr := fmt.Sprintf("%d/%d", date.Day(), date.Month())

	return fmt.Sprintf("%s %s: 🍯 **%s kg** // %s kg", dayAbbr, dateStr, yieldStr, sinceStr), nil
}

// buildWeeklyMessage creates the weekly summary notification
func (t *HourlySyncTask) buildWeeklyMessage(ctx context.Context, scaleUUID string, weekStart, weekEnd time.Time, loc *time.Location) (string, error) {
	// Sum hourly yields for each day of the week
	var weeklyYield sql.NullFloat64
	err := t.db.QueryRowContext(ctx, `
		SELECT SUM(yield) FROM wolf_hourly
		WHERE scale_id = $1
			AND DATE(timestamp AT TIME ZONE 'Europe/Copenhagen') >= $2
			AND DATE(timestamp AT TIME ZONE 'Europe/Copenhagen') <= $3
	`, scaleUUID, weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02")).Scan(&weeklyYield)
	if err != nil {
		return "", fmt.Errorf("failed to get weekly yield: %w", err)
	}

	sinceHarvest, err := t.getSinceHarvest(ctx, scaleUUID, weekEnd.Year(), loc)
	if err != nil {
		return "", fmt.Errorf("failed to get since-harvest: %w", err)
	}

	// Season total (same as since-harvest when no harvest has been recorded)
	seasonTotal, err := t.getSeasonTotal(ctx, scaleUUID, weekEnd.Year(), loc)
	if err != nil {
		return "", fmt.Errorf("failed to get season total: %w", err)
	}

	weight, err := t.getWeightAt(ctx, scaleUUID, weekEnd, loc)
	if err != nil {
		return "", fmt.Errorf("no weight for end of week %s: %w", weekEnd.Format("2006-01-02"), err)
	}

	inspectionStr := t.getLastInspection(ctx, scaleUUID)

	var weeklyYieldVal float64
	if weeklyYield.Valid {
		weeklyYieldVal = weeklyYield.Float64
	}
	yieldStr := formatYield(weeklyYieldVal)
	sinceStr := formatKg(sinceHarvest)
	totalStr := formatKg(seasonTotal)
	weightStr := formatKg(weight)

	return fmt.Sprintf(`%s %s: 🍯 **%s kg** // %s kg

**🍯 Total:** %s kg
**⚖️ Vægt:** %s kg
**🔍 Sidste inspektion:** %s`,
		danishWeekdays[weekEnd.Weekday()], fmt.Sprintf("%d/%d", weekEnd.Day(), weekEnd.Month()),
		yieldStr, sinceStr, totalStr, weightStr, inspectionStr), nil
}

func (t *HourlySyncTask) sendToMattermost(ctx context.Context, message string) error {
	payload := map[string]string{
		"username": t.config.Username,
		"text":     message,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.config.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mattermost returned status %d", resp.StatusCode)
	}

	t.logger.Info("Sent Mattermost notification")
	return nil
}
