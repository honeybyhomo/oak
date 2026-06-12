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
	dailyYield, err := getDailyYieldQuery(ctx, h.db, scaleUUID, date)
	if err != nil {
		return "", err
	}

	sinceHarvest, err := getSinceHarvestQuery(ctx, h.db, scaleUUID, date.Year(), loc)
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
	// Prefer measurement (corrected) yields for the week
	var weeklyYield sql.NullFloat64
	err := h.db.QueryRowContext(ctx, `
		SELECT SUM(yield) FROM wolf_measurement
		WHERE scale_id = $1 AND date >= $2 AND date <= $3
	`, scaleUUID, weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02")).Scan(&weeklyYield)
	if err != nil {
		return "", fmt.Errorf("failed to get weekly yield: %w", err)
	}

	// Season total since April 1
	seasonTotal, err := getSeasonTotalQuery(ctx, h.db, scaleUUID, weekEnd.Year(), loc)
	if err != nil {
		return "", fmt.Errorf("failed to get season total: %w", err)
	}

	// Total since last harvest
	sinceHarvest, err := getSinceHarvestQuery(ctx, h.db, scaleUUID, weekEnd.Year(), loc)
	if err != nil {
		return "", fmt.Errorf("failed to get since-harvest: %w", err)
	}

	weight, err := getWeightAtQuery(ctx, h.db, scaleUUID, weekEnd, loc)
	if err != nil {
		return "", fmt.Errorf("no weight for end of week %s: %w", weekEnd.Format("2006-01-02"), err)
	}

	inspectionStr := getLastInspectionQuery(ctx, h.db, scaleUUID)

	var weeklyYieldVal float64
	if weeklyYield.Valid {
		weeklyYieldVal = weeklyYield.Float64
	}
	yieldStr := formatYield(weeklyYieldVal)
	seasonStr := formatKg(seasonTotal)
	sinceStr := formatKg(sinceHarvest)
	weightStr := formatKg(weight)

	weekNum := formatISOWeek(weekEnd)

	return fmt.Sprintf(`Uge %s: 🍯 **%s kg**

**🍯 Total:** %s kg
**🍯 Total siden sidste høst:** %s kg
**⚖️ Vægt:** %s kg
**🔍 Sidste inspektion:** %s`,
		weekNum, yieldStr, seasonStr, sinceStr, weightStr, inspectionStr), nil
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
