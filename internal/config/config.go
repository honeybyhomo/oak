package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/knadh/koanf/parsers/toml/v2"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Config represents the complete application configuration
type Config struct {
	App       AppConfig       `koanf:"app"`
	Database  DatabaseConfig  `koanf:"database"`
	Logging   LoggingConfig   `koanf:"logging"`
	Scheduler SchedulerConfig `koanf:"scheduler"`
	Services  ServicesConfig  `koanf:"services"`
	Jobs      JobsConfig      `koanf:"jobs"`
}

type AppConfig struct {
	Environment string `koanf:"environment"`
	Port        int    `koanf:"port"`
}

type DatabaseConfig struct {
	Host         string `koanf:"host"`
	Port         int    `koanf:"port"`
	Name         string `koanf:"name"`
	User         string `koanf:"user"`
	Password     string `koanf:"password"`
	SSLMode      string `koanf:"ssl_mode"`
	MaxOpenConns int    `koanf:"max_open_conns"`
	MaxIdleConns int    `koanf:"max_idle_conns"`
}

type LoggingConfig struct {
	Level         string `koanf:"level"`
	Format        string `koanf:"format"`
	RetentionDays int    `koanf:"retention_days"`
}

type SchedulerConfig struct {
	Timezone string `koanf:"timezone"`
}

type ServicesConfig struct {
	Wolf WolfConfig `koanf:"wolf"`
}

type WolfConfig struct {
	// APIBaseURL is the base URL for the Wolf Waagen API
	APIBaseURL string `koanf:"api_base_url"`
}

type JobsConfig struct {
	Global         JobsGlobalConfig `koanf:"global"`
	WolfDaily      JobConfig        `koanf:"wolf_daily"`
	WolfHistorical JobConfig        `koanf:"wolf_historical"`
	WolfNotify     JobConfig        `koanf:"wolf_notify"`
	SystemCleanup  JobConfig        `koanf:"system_cleanup"`
	Mattermost     MattermostConfig `koanf:"mattermost"`
}

type JobsGlobalConfig struct {
	MaxAttempts     int `koanf:"max_attempts"`
	TimeoutMinutes  int `koanf:"timeout_minutes"`
	BatchInsertSize int `koanf:"batch_insert_size"`
}

type JobConfig struct {
	Schedules     []string `koanf:"schedules"`
	SnoozeSeconds int      `koanf:"snooze_seconds"`
}

type MattermostConfig struct {
	WebhookURL string `koanf:"webhook_url"`
	Username   string `koanf:"username"`
}

// Load reads configuration from config.toml and OS environment variables
// Environment variables with OAK__ prefix override config.toml values
// Example: OAK__DATABASE__PASSWORD -> database.password
func Load() (*Config, error) {
	k := koanf.New(".")

	// Load config.toml (required - base configuration)
	configPath := findFile("config.toml")
	if configPath == "" {
		return nil, fmt.Errorf("config.toml not found")
	}
	if err := k.Load(file.Provider(configPath), toml.Parser()); err != nil {
		return nil, fmt.Errorf("failed to load config.toml: %w", err)
	}

	// Load .env file (optional, overrides config.toml)
	envPath := findFile(".env")
	if envPath != "" {
		envMap, err := loadDotEnv(envPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load .env file: %w", err)
		}

		for key, value := range envMap {
			key = strings.TrimPrefix(key, "OAK__")
			key = strings.ReplaceAll(key, "__", ".")
			key = strings.ToLower(key)
			k.Set(key, value)
		}
	}

	// Load OS environment variables (overrides config.toml and .env)
	if err := k.Load(env.Provider("OAK__", ".", func(key string) string {
		key = strings.TrimPrefix(key, "OAK__")
		key = strings.ReplaceAll(key, "__", ".")
		return strings.ToLower(key)
	}), nil); err != nil {
		return nil, fmt.Errorf("failed to load environment variables: %w", err)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return &cfg, nil
}

// loadDotEnv reads and parses a .env file
func loadDotEnv(filename string) (map[string]string, error) {
	envMap := make(map[string]string)

	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[0] == value[len(value)-1] {
			value = value[1 : len(value)-1]
		}

		envMap[key] = value
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return envMap, nil
}

// findFile searches for a file in current and parent directory
func findFile(filename string) string {
	if _, err := os.Stat(filename); err == nil {
		return filename
	}
	parent := filepath.Join("..", filename)
	if _, err := os.Stat(parent); err == nil {
		return parent
	}
	return ""
}
