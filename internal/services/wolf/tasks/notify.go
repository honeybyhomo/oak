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

	for _, scaleID := range t.scales {
		message, err := t.buildMessage(ctx, scaleID, yesterday)
		if err != nil {
			t.logger.Error("Failed to build message", "scale", scaleID, "error", err)
			continue
		}

		if err := t.sendToMattermost(ctx, message); err != nil {
			return nil, fmt.Errorf("failed to send Mattermost notification: %w", err)
		}
	}

	return &job.Result{
		RecordsProcessed: len(t.scales),
		DurationTotal:    time.Since(start),
	}, nil
}

type reportData struct {
	Date           string
	Weight         sql.NullFloat64
	Yield          sql.NullFloat64
	SeasonTotal    sql.NullFloat64
	TempMin        sql.NullFloat64
	TempMax        sql.NullFloat64
	TempAvg        sql.NullFloat64
	LastInspection sql.NullTime
}

func (t *NotifyTask) buildMessage(ctx context.Context, scaleID string, date time.Time) (string, error) {
	var data reportData
	data.Date = date.Format("2. January 2006")

	// Get scale UUID
	var scaleUUID string
	err := t.db.QueryRowContext(ctx, `SELECT id FROM wolf_scale WHERE scale_id = $1`, scaleID).Scan(&scaleUUID)
	if err != nil {
		return "", fmt.Errorf("scale %s not found: %w", scaleID, err)
	}

	// Yesterday's measurement
	err = t.db.QueryRowContext(ctx, `
		SELECT weight, yield, temp_min, temp_max, temp_avg
		FROM wolf_measurement
		WHERE scale_id = $1 AND date = $2
	`, scaleUUID, date.Format("2006-01-02")).Scan(&data.Weight, &data.Yield, &data.TempMin, &data.TempMax, &data.TempAvg)
	if err != nil {
		return "", fmt.Errorf("no measurement for %s: %w", date.Format("2006-01-02"), err)
	}

	// Season total (from April 1st of current year)
	year := date.Year()
	seasonStart := fmt.Sprintf("%d-04-01", year)
	err = t.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(yield), 0)
		FROM wolf_measurement
		WHERE scale_id = $1 AND date >= $2
	`, scaleUUID, seasonStart).Scan(&data.SeasonTotal)
	if err != nil {
		return "", fmt.Errorf("failed to get season total: %w", err)
	}

	// Last inspection
	err = t.db.QueryRowContext(ctx, `
		SELECT date FROM wolf_checkup
		WHERE scale_id = $1
		ORDER BY date DESC
		LIMIT 1
	`, scaleUUID).Scan(&data.LastInspection)
	if err != nil && err != sql.ErrNoRows {
		t.logger.Warn("Failed to get last inspection", "error", err)
	}

	return t.formatMessage(data), nil
}

func (t *NotifyTask) formatMessage(data reportData) string {
	// Daily yield heading
	yieldStr := formatKg(data.Yield)
	heading := fmt.Sprintf("### 🍯 %s kg", yieldStr)

	// Season total and since-harvest (same for now — no harvest tracking yet)
	seasonStr := formatKg(data.SeasonTotal)

	// Weight
	weightStr := formatKg(data.Weight)

	// Temperature
	tempStr := "N/A"
	if data.TempMin.Valid && data.TempMax.Valid && data.TempAvg.Valid {
		tempStr = fmt.Sprintf("min %.1f°C · max %.1f°C · snit %.1f°C",
			data.TempMin.Float64, data.TempMax.Float64, data.TempAvg.Float64)
	}

	// Last inspection
	inspectionStr := "Ingen"
	if data.LastInspection.Valid {
		inspectionStr = data.LastInspection.Time.Format("02/01/2006")
	}

	return fmt.Sprintf(`%s

**🍯 Siden honningfratagning:** %s kg
**🍯 Total:** %s kg
**⚖️ Vægt:** %s kg
**🌡️ Temperatur:** %s
**📅 Dato:** %s
**🔍 Sidste inspektion:** %s`,
		heading, seasonStr, seasonStr, weightStr, tempStr, data.Date, inspectionStr)
}

func formatKg(v sql.NullFloat64) string {
	if !v.Valid {
		return "N/A"
	}
	return fmt.Sprintf("%.2f", v.Float64)
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
