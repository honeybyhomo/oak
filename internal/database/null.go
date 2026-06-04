package database

import (
	"database/sql"
	"time"
)

// NullString returns a sql.NullString with Valid=true if s is not empty
func NullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// NullTime returns a sql.NullTime with Valid=true if t is not zero
func NullTime(t time.Time) sql.NullTime {
	return sql.NullTime{Time: t, Valid: !t.IsZero()}
}

// NullInt32 returns a sql.NullInt32 with Valid=true
func NullInt32(i int32) sql.NullInt32 {
	return sql.NullInt32{Int32: i, Valid: true}
}

// NullInt64 returns a sql.NullInt64 with Valid=true
func NullInt64(i int64) sql.NullInt64 {
	return sql.NullInt64{Int64: i, Valid: true}
}

// NullFloat64 returns a sql.NullFloat64 with Valid=true
func NullFloat64(f float64) sql.NullFloat64 {
	return sql.NullFloat64{Float64: f, Valid: true}
}

// NullFloat64Ptr returns a sql.NullFloat64 with Valid=true only if pointer is not nil
func NullFloat64Ptr(f *float64) sql.NullFloat64 {
	if f == nil {
		return sql.NullFloat64{Valid: false}
	}
	return sql.NullFloat64{Float64: *f, Valid: true}
}
