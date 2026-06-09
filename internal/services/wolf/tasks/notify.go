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
	"github.com/honeybyhomo/oak/internal/job"
	"github.com/honeybyhomo/oak/internal/logger"
)

// NotifyTask sends a daily digest to Mattermost
type NotifyTask struct {
	db     *sql.DB
	config *config.MattermostConfig
	scales []string // scale IDs to report on
	logger *logger.Logger
}

// NewNotifyTask creates a new notification task
func NewNotifyTask(db *sql.DB, cfg *config.MattermostConfig, scales []string, log *logger.Logger) *NotifyTask {
	return &NotifyTask{
		db:     db,
		config: cfg,
		scales: scales,
		logger: log,
	}
}

func (t *NotifyTask) Name() string { return "wolf_notify" }

func (t *NotifyTask) ShouldRun() bool { return true }

func (t *NotifyTask) Run(ctx context.Context) (*job.Result, error) {
	start := time.Now()

	loc, err := time.LoadLocation("Europe/Copenhagen")
	if err != nil {
		return nil, fmt.Errorf("failed to load timezone: %w", err)
	}

	now := time.Now().In(loc)
	yesterday := now.AddDate(0, 0, -1)

	// Determine if this is a weekly notification (Monday = Sunday was last day of week)
	isWeekly := now.Weekday() == time.Monday

	var (
		message  string
		outerErr error
	)

	for _, scaleID := range t.scales {
		// Always send daily notification
		message, outerErr = t.buildDailyMessage(ctx, scaleID, yesterday)
		if outerErr != nil {
			t.logger.Error("Failed to build daily message", "scale", scaleID, "error", outerErr)
		} else if outerErr = t.sendToMattermost(ctx, message); outerErr != nil {
			return nil, fmt.Errorf("failed to send daily notification: %w", outerErr)
		}

		// On Monday, also send weekly summary
		if isWeekly {
			weekEnd := yesterday                   // Sunday
			weekStart := weekEnd.AddDate(0, 0, -6) // Monday
			message, outerErr = t.buildWeeklyMessage(ctx, scaleID, weekStart, weekEnd)
			if outerErr != nil {
				t.logger.Error("Failed to build weekly message", "scale", scaleID, "error", outerErr)
			} else if outerErr = t.sendToMattermost(ctx, message); outerErr != nil {
				return nil, fmt.Errorf("failed to send weekly notification: %w", outerErr)
			}
		}
	}

	return &job.Result{
		RecordsProcessed: len(t.scales),
		DurationTotal:    time.Since(start),
	}, nil
}

// getScaleUUID returns the internal UUID for a scale
func (t *NotifyTask) getScaleUUID(ctx context.Context, scaleID string) (string, error) {
	var uuid string
	err := t.db.QueryRowContext(ctx, `SELECT id FROM wolf_scale WHERE scale_id = $1`, scaleID).Scan(&uuid)
	if err != nil {
		return "", fmt.Errorf("scale %s not found: %w", scaleID, err)
	}
	return uuid, nil
}

// getSinceHarvest returns the yield sum since the last harvest (or April 1 if no harvest)
func (t *NotifyTask) getSinceHarvest(ctx context.Context, scaleUUID string, year int) (float64, error) {
	// Check for last harvest
	var lastHarvest sql.NullTime
	_ = t.db.QueryRowContext(ctx, `
		SELECT MAX(date) FROM wolf_harvest WHERE scale_id = $1
	`, scaleUUID).Scan(&lastHarvest)

	var since time.Time
	if lastHarvest.Valid {
		since = lastHarvest.Time
	} else {
		// No harvest — use April 1 of the current year
		since = time.Date(year, 4, 1, 0, 0, 0, 0, time.UTC)
	}

	var total sql.NullFloat64
	err := t.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(yield), 0)
		FROM wolf_measurement
		WHERE scale_id = $1 AND date > $2
	`, scaleUUID, since.Format("2006-01-02")).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total.Float64, nil
}

// getSeasonTotal returns the yield sum since April 1 of the given year
func (t *NotifyTask) getSeasonTotal(ctx context.Context, scaleUUID string, year int) (float64, error) {
	seasonStart := time.Date(year, 4, 1, 0, 0, 0, 0, time.UTC)
	var total sql.NullFloat64
	err := t.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(yield), 0)
		FROM wolf_measurement
		WHERE scale_id = $1 AND date >= $2
	`, scaleUUID, seasonStart.Format("2006-01-02")).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total.Float64, nil
}

// getLastInspection returns the date of the last checkup, formatted in Danish
func (t *NotifyTask) getLastInspection(ctx context.Context, scaleUUID string) string {
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

// formatDanishDate formats a date as "7. juni 2026"
func formatDanishDate(t time.Time) string {
	months := []string{
		"", "januar", "februar", "marts", "april", "maj", "juni",
		"juli", "august", "september", "oktober", "november", "december",
	}
	return fmt.Sprintf("%d. %s %d", t.Day(), months[t.Month()], t.Year())
}

// buildDailyMessage creates the daily one-liner notification
func (t *NotifyTask) buildDailyMessage(ctx context.Context, scaleID string, date time.Time) (string, error) {
	scaleUUID, err := t.getScaleUUID(ctx, scaleID)
	if err != nil {
		return "", err
	}

	// Yesterday's yield
	var dailyYield sql.NullFloat64
	err = t.db.QueryRowContext(ctx, `
		SELECT yield FROM wolf_measurement
		WHERE scale_id = $1 AND date = $2
	`, scaleUUID, date.Format("2006-01-02")).Scan(&dailyYield)
	if err != nil {
		return "", fmt.Errorf("no measurement for %s: %w", date.Format("2006-01-02"), err)
	}

	// Since last harvest
	sinceHarvest, err := t.getSinceHarvest(ctx, scaleUUID, date.Year())
	if err != nil {
		return "", fmt.Errorf("failed to get since-harvest: %w", err)
	}

	yieldStr := formatYield(dailyYield.Float64)
	sinceStr := formatKg(sinceHarvest)

	return fmt.Sprintf("### 🍯 %s kg // %s kg", yieldStr, sinceStr), nil
}

// buildWeeklyMessage creates the weekly summary notification
func (t *NotifyTask) buildWeeklyMessage(ctx context.Context, scaleID string, weekStart, weekEnd time.Time) (string, error) {
	scaleUUID, err := t.getScaleUUID(ctx, scaleID)
	if err != nil {
		return "", err
	}

	// Weekly yield (sum of the week)
	var weeklyYield sql.NullFloat64
	err = t.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(yield), 0) FROM wolf_measurement
		WHERE scale_id = $1 AND date >= $2 AND date <= $3
	`, scaleUUID, weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02")).Scan(&weeklyYield)
	if err != nil {
		return "", fmt.Errorf("failed to get weekly yield: %w", err)
	}

	// Since last harvest
	sinceHarvest, err := t.getSinceHarvest(ctx, scaleUUID, weekEnd.Year())
	if err != nil {
		return "", fmt.Errorf("failed to get since-harvest: %w", err)
	}

	// Season total
	seasonTotal, err := t.getSeasonTotal(ctx, scaleUUID, weekEnd.Year())
	if err != nil {
		return "", fmt.Errorf("failed to get season total: %w", err)
	}

	// End-of-week weight
	var weight sql.NullFloat64
	err = t.db.QueryRowContext(ctx, `
		SELECT weight FROM wolf_measurement
		WHERE scale_id = $1 AND date = $2
	`, scaleUUID, weekEnd.Format("2006-01-02")).Scan(&weight)
	if err != nil {
		return "", fmt.Errorf("no measurement for end of week %s: %w", weekEnd.Format("2006-01-02"), err)
	}

	// Last inspection
	inspectionStr := t.getLastInspection(ctx, scaleUUID)

	// Format
	yieldStr := formatYield(weeklyYield.Float64)
	sinceStr := formatKg(sinceHarvest)
	totalStr := formatKg(seasonTotal)
	weightStr := formatKg(weight.Float64)

	return fmt.Sprintf(`### 🍯 %s kg // %s kg

**🍯 Total:** %s kg
**⚖️ Vægt:** %s kg
**🔍 Sidste inspektion:** %s`,
		yieldStr, sinceStr, totalStr, weightStr, inspectionStr), nil
}

// formatKg formats a float as European-style kg (comma decimal, 2 decimals)
func formatKg(v float64) string {
	str := fmt.Sprintf("%.2f", v)
	// Replace period with comma for European number format
	for i := range str {
		if str[i] == '.' {
			str = str[:i] + "," + str[i+1:]
			break
		}
	}
	return str
}

// formatYield formats a yield value with explicit +/- sign and European decimal
func formatYield(v float64) string {
	str := formatKg(v)
	if v >= 0 {
		return "+" + str
	}
	return str // already has minus sign
}

func (t *NotifyTask) sendToMattermost(ctx context.Context, message string) error {
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
