// Package config loads application configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration for the API server.
type Config struct {
	AppPort        string
	DBUser         string
	DBPassword     string
	DBHost         string
	DBPort         string
	DBName         string
	JWTSecret      string
	JWTExpiry      time.Duration
	AllowedOrigins []string
}

// Load reads configuration from environment variables, applying sane
// defaults where possible. It returns an error if a required variable
// with no safe default is missing.
func Load() (*Config, error) {
	jwtExpiryHours, err := strconv.Atoi(getEnv("JWT_EXPIRY_HOURS", "24"))
	if err != nil || jwtExpiryHours <= 0 {
		return nil, fmt.Errorf("config: JWT_EXPIRY_HOURS must be a positive integer")
	}

	cfg := &Config{
		AppPort:        getEnv("APP_PORT", "8080"),
		DBUser:         getEnv("DB_USER", "root"),
		DBPassword:     os.Getenv("DB_PASSWORD"),
		DBHost:         getEnv("DB_HOST", "127.0.0.1"),
		DBPort:         getEnv("DB_PORT", "3306"),
		DBName:         getEnv("DB_NAME", "expense_tracker"),
		JWTSecret:      os.Getenv("JWT_SECRET"),
		JWTExpiry:      time.Duration(jwtExpiryHours) * time.Hour,
		AllowedOrigins: splitAndTrim(getEnv("ALLOWED_ORIGINS", "http://localhost:3000")),
	}

	if cfg.DBName == "" {
		return nil, fmt.Errorf("config: DB_NAME must not be empty")
	}

	// The JWT secret signs every issued token; a default fallback here
	// would make it trivial to forge tokens against any deployment that
	// forgot to set it, so we fail fast instead.
	if len(cfg.JWTSecret) < 32 {
		return nil, fmt.Errorf("config: JWT_SECRET must be set and at least 32 characters long")
	}

	return cfg, nil
}

// DSN builds a MySQL Data Source Name usable by the GORM MySQL driver.
func (c *Config) DSN() string {
	return fmt.Sprintf(
		"%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		c.DBUser, c.DBPassword, c.DBHost, c.DBPort, c.DBName,
	)
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

func splitAndTrim(csv string) []string {
	parts := strings.Split(csv, ",")
	origins := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			origins = append(origins, trimmed)
		}
	}
	return origins
}
