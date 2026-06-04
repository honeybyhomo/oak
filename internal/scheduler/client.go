package scheduler

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/honeybyhomo/oak/internal/config"
	"github.com/honeybyhomo/oak/internal/logger"
)

// Client wraps the PostgreSQL pool and River setup
type Client struct {
	pool   *pgxpool.Pool
	logger *logger.Logger
}

// NewClient creates a new scheduler client with a PostgreSQL connection pool
func NewClient(ctx context.Context, cfg *config.DatabaseConfig, log *logger.Logger) (*Client, error) {
	sslMode := cfg.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}

	connString := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		cfg.User, cfg.Password,
		cfg.Host, cfg.Port,
		cfg.Name, sslMode,
	)

	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		return nil, fmt.Errorf("failed to create PostgreSQL pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping PostgreSQL: %w", err)
	}

	log.Info("Connected to PostgreSQL for River job queue",
		"host", cfg.Host,
		"database", cfg.Name)

	return &Client{
		pool:   pool,
		logger: log,
	}, nil
}

// Pool returns the underlying pgxpool.Pool
func (c *Client) Pool() *pgxpool.Pool {
	return c.pool
}

// Migrate runs River's database migrations
func (c *Client) Migrate(ctx context.Context) error {
	migrator, err := rivermigrate.New(riverpgxv5.New(c.pool), nil)
	if err != nil {
		return fmt.Errorf("failed to create migrator: %w", err)
	}

	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	return nil
}

// CleanupStuckJobs cancels old jobs stuck in running/scheduled states
func (c *Client) CleanupStuckJobs(ctx context.Context) (int64, error) {
	result, err := c.pool.Exec(ctx, `
		UPDATE river_job 
		SET state = 'cancelled', finalized_at = NOW()
		WHERE state IN ('running', 'scheduled', 'retryable', 'pending', 'available')
		AND created_at < NOW() - INTERVAL '1 hour'
	`)
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup stuck jobs: %w", err)
	}
	return result.RowsAffected(), nil
}
