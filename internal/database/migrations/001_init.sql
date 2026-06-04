-- +goose Up

-- Scales (bee hive scales from Wolf Waagen)
CREATE TABLE wolf_scale (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    scale_id TEXT NOT NULL UNIQUE,       -- Wolf Waagen scale ID (e.g. 'G58E19')
    name TEXT NOT NULL,                  -- Human-readable name (e.g. 'Valby')
    latitude NUMERIC(8, 4),
    longitude NUMERIC(8, 4),
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Daily measurements (one row per scale per day)
CREATE TABLE wolf_measurement (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    scale_id UUID NOT NULL REFERENCES wolf_scale(id),
    date DATE NOT NULL,
    weight NUMERIC(7, 3),               -- end-of-day weight in kg
    yield NUMERIC(7, 3),                -- daily yield in kg (already corrected for inspections)
    temp_min NUMERIC(4, 1),
    temp_max NUMERIC(4, 1),
    temp_avg NUMERIC(4, 1),
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(scale_id, date)
);

-- Checkup items (scale on/off events — inspections, harvests)
CREATE TABLE wolf_checkup (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    scale_id UUID NOT NULL REFERENCES wolf_scale(id),
    external_id TEXT NOT NULL,           -- ID from the Wolf API (e.g. 'exnRzwQvMnGZ')
    date DATE NOT NULL,
    time_begin TEXT,                     -- e.g. '17:40'
    time_end TEXT,                       -- e.g. '17:44'
    weight_begin NUMERIC(7, 3),          -- weight when scale turned off
    weight_end NUMERIC(7, 3),            -- weight when scale turned back on
    checkup_type TEXT,                   -- e.g. 'inspection'
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(scale_id, external_id)
);

-- Honey harvests (manually recorded by the beekeeper)
CREATE TABLE wolf_harvest (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    scale_id UUID NOT NULL REFERENCES wolf_scale(id),
    date DATE NOT NULL,
    weight_kg NUMERIC(7, 3),            -- how much honey was taken
    notes TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Seed the scale
INSERT INTO wolf_scale (scale_id, name, latitude, longitude)
VALUES ('G58E19', 'Valby', 55.6, 12.5);

-- +goose Down

DROP TABLE IF EXISTS wolf_harvest;
DROP TABLE IF EXISTS wolf_checkup;
DROP TABLE IF EXISTS wolf_measurement;
DROP TABLE IF EXISTS wolf_scale;
