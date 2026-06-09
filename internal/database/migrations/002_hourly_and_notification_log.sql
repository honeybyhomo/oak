-- +goose Up

-- Hourly measurements (one row per scale per hour)
CREATE TABLE wolf_hourly (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    scale_id UUID NOT NULL REFERENCES wolf_scale(id),
    timestamp TIMESTAMPTZ NOT NULL,      -- exact hour timestamp (CEST/CET)
    weight NUMERIC(7, 3),                -- weight at this hour in kg
    yield NUMERIC(7, 3),                 -- hourly yield in kg
    yield_sum NUMERIC(7, 3),             -- cumulative daily yield at this hour
    temperature NUMERIC(4, 1),           -- temperature at this hour
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(scale_id, timestamp)
);

-- Notification log (track which notifications have been sent)
CREATE TABLE wolf_notification_log (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    scale_id UUID NOT NULL REFERENCES wolf_scale(id),
    date DATE NOT NULL,                  -- the date the notification is about (yesterday)
    notification_type TEXT NOT NULL,     -- 'daily' or 'weekly'
    sent_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(scale_id, date, notification_type)
);

-- +goose Down

DROP TABLE IF EXISTS wolf_notification_log;
DROP TABLE IF EXISTS wolf_hourly;
