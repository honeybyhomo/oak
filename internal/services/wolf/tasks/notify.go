package tasks

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/honeybyhomo/oak/internal/config"
	"github.com/honeybyhomo/oak/internal/logger"
)

// NotifyHelper exposes notification message building and sending for use by the CLI.
// Unlike HourlySyncTask, it does not log to wolf_notification_log.
type NotifyHelper struct {
	db     *sql.DB
	config *config.MattermostConfig
	logger *logger.Logger
}

// NewNotifyHelper creates a new NotifyHelper
func NewNotifyHelper(db *sql.DB, cfg *config.MattermostConfig, log *logger.Logger) *NotifyHelper {
	return &NotifyHelper{db: db, config: cfg, logger: log}
}

// BuildDailyMessage builds the daily notification message for the given date.
func (h *NotifyHelper) BuildDailyMessage(ctx context.Context, scaleUUID string, date time.Time, loc *time.Location) (string, error) {
	dailyYield, err := h.getDailyYield(ctx, scaleUUID, date)
	if err != nil {
		return "", err
	}

	sinceHarvest, err := h.getSinceHarvest(ctx, scaleUUID, date.Year(), loc)
	if err != nil {
		return "", fmt.Errorf("failed to get since-harvest: %w", err)
	}

	yieldStr := formatYield(dailyYield)
	sinceStr := formatKg(sinceHarvest)
	dayAbbr := danishWeekdays[date.Weekday()]
	dateStr := fmt.Sprintf("%d/%d", date.Day(), date.Month())

	return fmt.Sprintf("%s %s: 🍯 **%s kg** // %s kg", dayAbbr, dateStr, yieldStr, sinceStr), nil
}

// BuildWeeklyMessage builds the weekly summary message for the given date range.
func (h *NotifyHelper) BuildWeeklyMessage(ctx context.Context, scaleUUID string, weekStart, weekEnd time.Time, loc *time.Location) (string, error) {
	var weeklyYield sql.NullFloat64
	err := h.db.QueryRowContext(ctx, `
		SELECT SUM(yield) FROM wolf_hourly
		WHERE scale_id = $1
			AND DATE(timestamp AT TIME ZONE 'Europe/Copenhagen') >= $2
			AND DATE(timestamp AT TIME ZONE 'Europe/Copenhagen') <= $3
	`, scaleUUID, weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02")).Scan(&weeklyYield)
	if err != nil {
		return "", fmt.Errorf("failed to get weekly yield: %w", err)
	}

	sinceHarvest, err := h.getSinceHarvest(ctx, scaleUUID, weekEnd.Year(), loc)
	if err != nil {
		return "", fmt.Errorf("failed to get since-harvest: %w", err)
	}

	seasonTotal, err := h.getSeasonTotal(ctx, scaleUUID, weekEnd.Year(), loc)
	if err != nil {
		return "", fmt.Errorf("failed to get season total: %w", err)
	}

	weight, err := h.getWeightAt(ctx, scaleUUID, weekEnd, loc)
	if err != nil {
		return "", fmt.Errorf("no weight for end of week %s: %w", weekEnd.Format("2006-01-02"), err)
	}

	inspectionStr := h.getLastInspection(ctx, scaleUUID)

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

// SendToMattermost sends a message to the configured Mattermost webhook.
func (h *NotifyHelper) SendToMattermost(ctx context.Context, message string) error {
	payload := map[string]string{
		"username": h.config.Username,
		"text":     message,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.config.WebhookURL, bytes.NewReader(body))
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

	return nil
}

// DanishWeekdays returns the Danish weekday abbreviation map (exported for CLI use).
func DanishWeekdays() map[time.Weekday]string {
	return danishWeekdays
}

// --- Reuse the same query logic as HourlySyncTask ---

func (h *NotifyHelper) getDailyYield(ctx context.Context, scaleUUID string, date time.Time) (float64, error) {
	var total sql.NullFloat64
	err := h.db.QueryRowContext(ctx, `
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

func (h *NotifyHelper) getSinceHarvest(ctx context.Context, scaleUUID string, year int, loc *time.Location) (float64, error) {
	var lastHarvest sql.NullTime
	_ = h.db.QueryRowContext(ctx, `
		SELECT MAX(date) FROM wolf_harvest WHERE scale_id = $1
	`, scaleUUID).Scan(&lastHarvest)

	var since time.Time
	if lastHarvest.Valid {
		since = lastHarvest.Time
	} else {
		since = time.Date(year, 4, 1, 0, 0, 0, 0, loc)
	}

	sinceStr := since.Format("2006-01-02")

	var hourlyTotal sql.NullFloat64
	err := h.db.QueryRowContext(ctx, `
		SELECT SUM(yield) FROM wolf_hourly
		WHERE scale_id = $1 AND DATE(timestamp AT TIME ZONE 'Europe/Copenhagen') > $2
	`, scaleUUID, sinceStr).Scan(&hourlyTotal)
	if err != nil {
		return 0, err
	}

	var measurementTotal sql.NullFloat64
	err = h.db.QueryRowContext(ctx, `
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

func (h *NotifyHelper) getSeasonTotal(ctx context.Context, scaleUUID string, year int, loc *time.Location) (float64, error) {
	seasonStart := time.Date(year, 4, 1, 0, 0, 0, 0, loc)
	sinceStr := seasonStart.Format("2006-01-02")

	var hourlyTotal sql.NullFloat64
	err := h.db.QueryRowContext(ctx, `
		SELECT SUM(yield) FROM wolf_hourly
		WHERE scale_id = $1 AND DATE(timestamp AT TIME ZONE 'Europe/Copenhagen') > $2
	`, scaleUUID, sinceStr).Scan(&hourlyTotal)
	if err != nil {
		return 0, err
	}

	var measurementTotal sql.NullFloat64
	err = h.db.QueryRowContext(ctx, `
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

func (h *NotifyHelper) getWeightAt(ctx context.Context, scaleUUID string, date time.Time, loc *time.Location) (float64, error) {
	hour23 := time.Date(date.Year(), date.Month(), date.Day(), 23, 0, 0, 0, loc)
	var weight sql.NullFloat64
	err := h.db.QueryRowContext(ctx, `
		SELECT weight FROM wolf_hourly
		WHERE scale_id = $1 AND timestamp = $2
	`, scaleUUID, hour23.UTC()).Scan(&weight)
	if err != nil {
		return 0, fmt.Errorf("no weight for %s: %w", date.Format("2006-01-02"), err)
	}
	return weight.Float64, nil
}

func (h *NotifyHelper) getLastInspection(ctx context.Context, scaleUUID string) string {
	var lastDate sql.NullTime
	err := h.db.QueryRowContext(ctx, `
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
