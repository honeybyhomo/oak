package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/honeybyhomo/oak/internal/config"
	"github.com/honeybyhomo/oak/internal/database"
	"github.com/honeybyhomo/oak/internal/logger"
	"github.com/honeybyhomo/oak/internal/services/wolf/tasks"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: oak notify <daily|weekly|both> [--dry-run]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Sends notification(s) for the most recent date with complete hourly data.")
		fmt.Fprintln(os.Stderr, "Use --dry-run to preview without sending to Mattermost.")
		os.Exit(1)
	}

	notifType := os.Args[1]
	dryRun := len(os.Args) > 2 && os.Args[2] == "--dry-run"

	if notifType != "daily" && notifType != "weekly" && notifType != "both" {
		fmt.Fprintf(os.Stderr, "Unknown type %q — must be daily, weekly, or both\n", notifType)
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	log := logger.New(&cfg.Logging)

	db, err := database.New(&cfg.Database)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	loc, err := time.LoadLocation("Europe/Copenhagen")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load timezone: %v\n", err)
		os.Exit(1)
	}

	scaleID := "G58E19"

	// Find scale UUID
	var scaleUUID string
	err = db.QueryRowContext(context.Background(), `SELECT id FROM wolf_scale WHERE scale_id = $1`, scaleID).Scan(&scaleUUID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Scale %s not found: %v\n", scaleID, err)
		os.Exit(1)
	}

	// Find the most recent date with a complete day (23:00 entry)
	date, err := findMostRecentCompleteDay(db.DB, scaleUUID, loc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to find complete day: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Most recent complete day: %s (%s)\n\n", date.Format("2006-01-02"), tasks.DanishWeekdays()[date.Weekday()])

	// Build a fake task to reuse the message-building logic
	helper := tasks.NewNotifyHelper(db.DB, &cfg.Jobs.Mattermost, log)

	if notifType == "daily" || notifType == "both" {
		msg, err := helper.BuildDailyMessage(context.Background(), scaleUUID, date, loc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to build daily message: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("=== DAILY ===")
		fmt.Println(msg)
		fmt.Println()
		if !dryRun {
			if err := helper.SendToMattermost(context.Background(), msg); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to send: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("✓ Sent to Mattermost")
		}
	}

	if notifType == "weekly" || notifType == "both" {
		// Find the most recent completed week (ending on Sunday with 23:00 data)
		weekEnd, err := findMostRecentCompleteSunday(db.DB, scaleUUID, loc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to find completed week: %v\n", err)
			os.Exit(1)
		}
		weekStart := weekEnd.AddDate(0, 0, -6) // Monday

		fmt.Printf("Weekly for week %s (%s – %s)\n", tasks.FormatISOWeek(weekEnd), weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02"))

		msg, err := helper.BuildWeeklyMessage(context.Background(), scaleUUID, weekStart, weekEnd, loc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to build weekly message: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("=== WEEKLY ===")
		fmt.Println(msg)
		fmt.Println()
		if !dryRun {
			if err := helper.SendToMattermost(context.Background(), msg); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to send: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("✓ Sent to Mattermost")
		}
	}

	if dryRun {
		fmt.Println("(dry-run — no messages sent)")
	}
}

func findMostRecentCompleteDay(db *sql.DB, scaleUUID string, loc *time.Location) (time.Time, error) {
	var ts time.Time
	err := db.QueryRowContext(context.Background(), `
		SELECT MAX(DATE(timestamp AT TIME ZONE 'Europe/Copenhagen'))
		FROM wolf_hourly
		WHERE scale_id = $1
			AND EXTRACT(HOUR FROM timestamp AT TIME ZONE 'Europe/Copenhagen') = 23
			AND yield IS NOT NULL
	`, scaleUUID).Scan(&ts)
	if err != nil {
		return time.Time{}, fmt.Errorf("query failed: %w", err)
	}
	if ts.IsZero() {
		return time.Time{}, fmt.Errorf("no complete days found")
	}
	return ts, nil
}

func findMostRecentCompleteSunday(db *sql.DB, scaleUUID string, loc *time.Location) (time.Time, error) {
	var ts time.Time
	err := db.QueryRowContext(context.Background(), `
		SELECT MAX(DATE(timestamp AT TIME ZONE 'Europe/Copenhagen'))
		FROM wolf_hourly
		WHERE scale_id = $1
			AND EXTRACT(HOUR FROM timestamp AT TIME ZONE 'Europe/Copenhagen') = 23
			AND yield IS NOT NULL
			AND EXTRACT(DOW FROM timestamp AT TIME ZONE 'Europe/Copenhagen') = 0
	`, scaleUUID).Scan(&ts)
	if err != nil {
		return time.Time{}, fmt.Errorf("query failed: %w", err)
	}
	if ts.IsZero() {
		return time.Time{}, fmt.Errorf("no complete Sunday found")
	}
	return ts, nil
}
