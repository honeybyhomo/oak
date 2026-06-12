package tasks

import (
	"fmt"
	"time"
)

// danishWeekdays maps time.Weekday to Danish abbreviations
var danishWeekdays = map[time.Weekday]string{
	time.Monday:    "Man",
	time.Tuesday:   "Tir",
	time.Wednesday: "Ons",
	time.Thursday:  "Tor",
	time.Friday:    "Fre",
	time.Saturday:  "Lør",
	time.Sunday:    "Søn",
}

// FormatISOWeek returns the ISO week number for a date (exported for CLI use)
func FormatISOWeek(t time.Time) string {
	_, week := t.ISOWeek()
	return fmt.Sprintf("%d", week)
}

// formatISOWeek returns the ISO week number for a date (e.g., "24")
func formatISOWeek(t time.Time) string {
	return FormatISOWeek(t)
}

// formatDanishDate formats a date as "7. juni 2026"
func formatDanishDate(t time.Time) string {
	months := []string{
		"", "januar", "februar", "marts", "april", "maj", "juni",
		"juli", "august", "september", "oktober", "november", "december",
	}
	return fmt.Sprintf("%d. %s %d", t.Day(), months[t.Month()], t.Year())
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
