# AGENTS.md

## Project Overview

Oak is a personal automation platform built in Go. It fetches data from external APIs, stores it in PostgreSQL, and sends notifications to Mattermost. The project uses a task/job architecture with River (PostgreSQL-backed job queue) for scheduling.

Currently the only integration is **Wolf Waagen** — a bee hive scale that provides weight, yield, and temperature data via a public API.

## Build/Lint/Test Commands

```bash
make build          # Build binary to ./bin/oak
make run            # Run the application
make run-dev        # Run in development mode (sources .env.dev)
make fmt            # Format Go code
make deps           # Download dependencies and tidy go.mod
make test           # Run all tests
make clean          # Remove build artifacts
```

## Tech Stack

- **Go** 1.26
- **PostgreSQL** (via `postgres-apps` shared instance) — both app data and River job queue
- **River** — job queue (github.com/riverqueue/river)
- **SQLC** — type-safe SQL queries (generate with `make generate` if queries are added)
- **Koanf** — config (TOML + env vars with `OAK__` prefix)
- **CharmLog** — logging (github.com/charmbracelet/log)
- **Resty** v3 — HTTP client
- **Goose** — database migrations (embedded in binary)

## Project Structure

```
cmd/main.go                              # Entry point (River + HTTP health check)
internal/
  config/config.go                       # Config (OAK__ prefix, koanf, TOML + env)
  database/
    db.go                                # PostgreSQL connection (pgx/stdlib)
    migrate.go                           # Goose migrations (embedded)
    null.go                              # sql.Null* helpers
    migrations/001_init.sql              # wolf_scale, wolf_measurement, wolf_checkup, wolf_harvest
    migrations/002_hourly_and_notification_log.sql  # wolf_hourly, wolf_notification_log
  job/                                   # Job framework (simplified, no full sync concept)
    types.go                             # Task, Result, Definition
    registry.go                          # Job registration
    scheduler.go                         # Cron → River periodic jobs
    worker.go                            # River worker that executes tasks
  jobs/register.go                       # Registers all jobs
  logger/                                # CharmLog + slog adapter (for River)
  scheduler/client.go                    # pgxpool + River setup/migration
  services/wolf/
    client.go                            # Wolf Waagen API client
    tasks/
      hourly_sync.go                   # Fetch hourly data + send notifications when data is complete
      backfill.go                        # Historical data fetch (year-by-year)
      format.go                           # Formatting helpers (Danish dates, European numbers)
compose.yaml                             # Traefik + postgres-apps networks
Dockerfile                               # Multi-stage build
config.toml                              # All config (secrets = placeholder values)
.env                                     # Production secrets (placeholder values, real ones in Dockhand)
.env.dev                                 # Local dev config (gitignored)
```

## Database

Oak uses the shared `postgres-apps` instance on TrueNAS. Connection details:

- **Host**: `postgres-apps` (in Docker) / `10.10.10.10:5432` (from laptop)
- **Database**: `oak`
- **User**: `oak`
- **Password**: In Dockhand secrets / `.env.dev`

### Schema

| Table | Purpose |
|-------|---------|
| `wolf_scale` | Bee hive scales (UUID, scale ID, name, GPS) |
| `wolf_hourly` | Hourly measurements (weight, yield, yield_sum, temperature) |
| `wolf_measurement` | Daily measurements (legacy, no longer written to) |
| `wolf_checkup` | Scale on/off events (inspections) from the API |
| `wolf_harvest` | Manually recorded honey harvests (not yet in use) |
| `wolf_notification_log` | Tracks which daily/weekly notifications have been sent |
| `river_job` | River's job queue table |

### Migrations

Migrations are embedded in the binary and run automatically on startup via Goose. To add a new migration:

```bash
# Create migration file
goose -dir internal/database/migrations create <name> sql

# Or manually create: internal/database/migrations/002_<name>.sql
# Format: -- +goose Up / -- +goose Down
```

## Jobs

| Job | Schedule | Description |
|-----|----------|-------------|
| `wolf_hourly` | Every hour | Fetch hourly data from API → upsert into DB. Checks if yesterday's data is complete (has 23:00 entry) → sends daily notification. On Monday also sends weekly summary. |
| `wolf_backfill` | Manual only | Fetch historical data year-by-year from 2016 to yesterday (uses daily endpoint) |

### Triggering manual jobs

Jobs without schedules can be triggered by inserting directly into River:

```bash
# SSH into TrueNAS or use docker exec
docker exec postgres-apps psql -U oak -d oak -c \
  "INSERT INTO river_job (kind, state, args, priority, queue, max_attempts, created_at) VALUES ('oak_job', 'available', '{\"kind\":\"wolf_backfill\"}', 1, 'default', 1, NOW());"
```

## Wolf Waagen API

The bee scale has a public API — no authentication needed.

### Endpoint
```
GET https://app.wolf-waagen.de/graph/stock/{scaleId}?interval=day&start={epoch_ms}&end={epoch_ms}
```

### Important details
- `interval=day` returns pre-aggregated daily values (weight = 00:00 reading, yield = corrected daily change)
- `interval=hour` returns hourly readings with weight, yield, yield_sum, temperature. **Note:** `yield` is the hourly change; `yield_sum` is cumulative relative to the fetch window start (NOT a daily reset). Daily yield must be calculated as `SUM(yield)` across all hours of the day.
- The daily endpoint's pre-aggregated yield may change throughout the day as more hourly data arrives. We use `interval=hour` and calculate the daily yield as `SUM(yield)` across all hourly entries for the day.
- `values` field can be: `null`, numbers, strings, arrays of numbers, or objects — handled with `json.RawMessage`
- Timestamps are epoch milliseconds. Use Copenhagen timezone (CEST/CET) for midnight boundaries.
- The scale transmits data every ~6 hours. The 23:00 reading for a day may not arrive until late morning or afternoon the next day — timing varies.
- We store hourly data and send the daily notification only once the 23:00 entry is available.

### Scale IDs
- `G58E19` — "Valby" (55.6°N, 12.5°E), data since 2023

## Notifications

### Daily (every day including Monday)
```
Søn 8/6: 🍯 **-0,05 kg** // 6,27 kg
```
One line: `[weekday date]: 🍯 **[daily yield]** // [since last harvest (or April 1)]`

Sent once the 23:00 hourly entry for yesterday is available. Tracked in `wolf_notification_log`.

### Weekly (Monday, extra message)
```
Søn 8/6: 🍯 **-0,09 kg** // 6,27 kg

**🍯 Total:** 10,55 kg
**⚖️ Vægt:** 49,285 kg
**🔍 Sidste inspektion:** 7. juni 2026
```
First line is the daily, followed by season total, weight, and last inspection.

### Number format
- European: comma decimal (0,04 not 0.04)
- Yield always has explicit sign: +0,04 or -0,15

## Deployment

Deployed via **Dockhand** as a Git stack watching `honeybyhomo/oak` on `main`. Push to main triggers auto-deploy via GitHub webhook.

### Dockhand secrets
| Secret | Description |
|--------|-------------|
| `DB_PASSWORD` | PostgreSQL password for the `oak` user |
| `MATTERMOST_WEBHOOK_URL` | Incoming webhook URL for the biavl channel |

### Rebuilding after code changes

Dockhand has `buildOnDeploy: true` enabled for the Oak stack. Every push to `main` triggers a fresh `docker compose build` + `up`, so code changes are picked up automatically.

## Code Style Guidelines

### Imports
Group in order: standard library → external packages → internal packages

### Error Handling
Wrap errors with context using `fmt.Errorf`:
```go
if err != nil {
    return fmt.Errorf("failed to fetch measurements: %w", err)
}
```

### Logging
Use operation tracking for measurable operations:
```go
op := log.Track("Fetch measurements from Wolf API")
// ... do work
op.Complete(records)
```

### Naming
- Packages: `flatcase` (e.g., `wolf`, `config`, `logger`)
- Exported: `PascalCase`
- Internal: `camelCase`
- Database tables: `snake_case` with `wolf_` prefix
- Files: `snake_case.go`

## Configuration

Environment variables use `OAK__` prefix with double-underscore for nesting:
```
OAK__DATABASE__PASSWORD → config.database.password
OAK__JOBS__MATTERMOST__WEBHOOK_URL → config.jobs.mattermost.webhook_url
```

Config sources (in priority order):
1. OS environment variables (`OAK__*`)
2. `.env` file (loaded by app, overrides config.toml)
3. `config.toml` (base defaults, baked into Docker image)
