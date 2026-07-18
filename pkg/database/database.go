// Package database initializes and configures the GORM connection pool
// used across the application.
package database

import (
	"fmt"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Options controls how the underlying *sql.DB connection pool is tuned.
// Sensible defaults are applied by NewMySQLConnection when a field is left
// at its zero value.
type Options struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

func (o Options) withDefaults() Options {
	if o.MaxOpenConns == 0 {
		o.MaxOpenConns = 25
	}
	if o.MaxIdleConns == 0 {
		o.MaxIdleConns = 10
	}
	if o.ConnMaxLifetime == 0 {
		o.ConnMaxLifetime = 5 * time.Minute
	}
	return o
}

// NewMySQLConnection opens a GORM connection to MySQL using the provided
// DSN, configures the underlying connection pool, and returns a ready to
// use *gorm.DB. The caller owns the returned connection and is responsible
// for closing it (see Close) on shutdown.
func NewMySQLConnection(dsn string, opts Options) (*gorm.DB, error) {
	opts = opts.withDefaults()

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
		// TranslateError lets callers check sentinel errors such as
		// gorm.ErrDuplicatedKey instead of parsing raw MySQL driver errors.
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("database: failed to open connection: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("database: failed to access underlying sql.DB: %w", err)
	}

	sqlDB.SetMaxOpenConns(opts.MaxOpenConns)
	sqlDB.SetMaxIdleConns(opts.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(opts.ConnMaxLifetime)

	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("database: failed to ping database: %w", err)
	}

	return db, nil
}

// Close releases the underlying database connection pool. It should be
// deferred in main() right after a successful NewMySQLConnection call.
func Close(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("database: failed to access underlying sql.DB: %w", err)
	}
	if err := sqlDB.Close(); err != nil {
		return fmt.Errorf("database: failed to close connection: %w", err)
	}
	return nil
}
