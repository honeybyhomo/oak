// Package wolf provides a client and tasks for syncing data from the Wolf Waagen API.
// It fetches daily measurements and checkup items from bee hive scales.
package wolf

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"resty.dev/v3"

	"github.com/honeybyhomo/oak/internal/config"
	"github.com/honeybyhomo/oak/internal/logger"
)

// Client wraps the Wolf Waagen API client
type Client struct {
	client *resty.Client
	config *config.WolfConfig
	logger *logger.Logger
}

// New creates a new Wolf Waagen API client
func New(cfg *config.WolfConfig, log *logger.Logger) *Client {
	client := resty.New()
	client.SetBaseURL(cfg.APIBaseURL)
	client.SetTimeout(30 * time.Second)
	client.SetRetryCount(3)
	client.SetRetryWaitTime(2 * time.Second)
	client.SetRetryMaxWaitTime(10 * time.Second)

	log.Debug("Wolf Waagen client initialized",
		"base_url", cfg.APIBaseURL)

	return &Client{
		client: client,
		config: cfg,
		logger: log,
	}
}

// APIResponse represents the full response from the Wolf Waagen graph API
type APIResponse struct {
	ScaleID        string    `json:"scaleId"`
	Start          int64     `json:"start"`
	End            int64     `json:"end"`
	PointStart     int64     `json:"pointStart"`
	PointInterval  int       `json:"pointInterval"`
	PointIntervalU string    `json:"pointIntervalUnit"`
	Series         []Series  `json:"series"`
	Summary        []Summary `json:"summary"`
	CheckupItems   []Checkup `json:"checkupItems"`
}

// Series represents a single data series in the API response
type Series struct {
	ID     string       `json:"id"`
	Unit   string       `json:"unit"`
	Values []FloatValue `json:"values,omitempty"`
}

// FloatValue handles JSON values that can be either numbers or strings
// The Wolf API returns temperature_glt as strings like "1032.0"
type FloatValue float64

func (f *FloatValue) UnmarshalJSON(data []byte) error {
	// Try number first
	var num float64
	if err := json.Unmarshal(data, &num); err == nil {
		*f = FloatValue(num)
		return nil
	}
	// Try string
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		if s == "" {
			*f = FloatValue(math.NaN())
			return nil
		}
		parsed, err := strconv.ParseFloat(s, 64)
		if err != nil {
			*f = FloatValue(math.NaN())
			return nil
		}
		*f = FloatValue(parsed)
		return nil
	}
	// null
	*f = FloatValue(math.NaN())
	return nil
}

// Float returns the float64 value, or NaN if not valid
func (f FloatValue) Float() float64 {
	return float64(f)
}

// IsValid returns true if the value is a real number (not NaN)
func (f FloatValue) IsValid() bool {
	return !math.IsNaN(float64(f))
}

// Summary contains pre-aggregated statistics
type Summary struct {
	ID   string        `json:"id"`
	Unit string        `json:"unit"`
	Data []SummaryItem `json:"data"`
}

// SummaryItem is a single aggregated value
type SummaryItem struct {
	Type  string  `json:"type"`
	Value float64 `json:"value"`
}

// Checkup represents a scale on/off event (inspection, harvest, etc.)
type Checkup struct {
	ID          string  `json:"id"`
	Date        string  `json:"date"`
	TimeBegin   string  `json:"time_begin"`
	TimeEnd     string  `json:"time_end"`
	WeightBegin float64 `json:"weight_begin"`
	WeightEnd   float64 `json:"weight_end"`
	Comment     *string `json:"comment"`
	Priority    int     `json:"priority"`
	Type        string  `json:"type"`
}

// FetchDailyData fetches daily measurements for a scale between start and end dates
func (c *Client) FetchDailyData(ctx context.Context, scaleID string, start, end time.Time) (*APIResponse, error) {
	startMs := start.UnixMilli()
	endMs := end.UnixMilli()

	url := fmt.Sprintf("/graph/stock/%s?interval=day&start=%d&end=%d",
		scaleID, startMs, endMs)

	var resp APIResponse
	r, err := c.client.R().
		SetContext(ctx).
		SetResult(&resp).
		Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch daily data: %w", err)
	}

	if r.StatusCode() != 200 {
		return nil, fmt.Errorf("API returned status %d: %s", r.StatusCode(), r.String())
	}

	return &resp, nil
}

// FindSeries returns a series by ID from the API response
func FindSeries(resp *APIResponse, id string) *Series {
	for i := range resp.Series {
		if resp.Series[i].ID == id {
			return &resp.Series[i]
		}
	}
	return nil
}

// FindSummary returns a summary by ID from the API response
func FindSummary(resp *APIResponse, id string) *Summary {
	for i := range resp.Summary {
		if resp.Summary[i].ID == id {
			return &resp.Summary[i]
		}
	}
	return nil
}
