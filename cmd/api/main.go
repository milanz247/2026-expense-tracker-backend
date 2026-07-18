// Command api is the entry point for the Financial Expenses & Income
// Tracker backend. It wires configuration, the database connection, and
// HTTP handlers together, then serves requests over net/http.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/milan/expense-tracker/backend/internal/account"
	"github.com/milan/expense-tracker/backend/internal/category"
	"github.com/milan/expense-tracker/backend/internal/debt"
	"github.com/milan/expense-tracker/backend/internal/middleware"
	"github.com/milan/expense-tracker/backend/internal/report"
	"github.com/milan/expense-tracker/backend/internal/store"
	"github.com/milan/expense-tracker/backend/internal/transaction"
	"github.com/milan/expense-tracker/backend/internal/user"
	"github.com/milan/expense-tracker/backend/pkg/auth"
	"github.com/milan/expense-tracker/backend/pkg/config"
	"github.com/milan/expense-tracker/backend/pkg/database"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// Load variables from a local .env file if one is present. This is a
	// no-op in production, where real environment variables are set by the
	// deployment platform and take precedence over anything in .env.
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		logger.Warn("failed to load .env file", "error", err)
	}

	if err := run(logger); err != nil {
		logger.Error("server exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := database.NewMySQLConnection(cfg.DSN(), database.Options{})
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := database.Close(db); closeErr != nil {
			logger.Error("failed to close database connection", "error", closeErr)
		}
	}()

	// AutoMigrate keeps the schema in sync with the User, Account,
	// Category, Transaction, Debt, Repayment, Creditor, Purchase, and
	// Settlement models. Order matters: User first (Account and Category
	// both reference it), then Account and Category (Transaction
	// references both), then Debt and Repayment (Repayment references
	// both Debt and Account), then Creditor, Purchase, and Settlement
	// (Purchase references Creditor/Transaction/Category; Settlement
	// references Creditor/Account). In a production deployment this is
	// typically replaced by versioned SQL migrations, but is convenient
	// for early-stage development.
	if err := db.AutoMigrate(
		&user.User{},
		&account.Account{},
		&category.Category{},
		&transaction.Transaction{},
		&debt.Debt{},
		&debt.Repayment{},
		&store.Creditor{},
		&store.Purchase{},
		&store.Settlement{},
	); err != nil {
		return err
	}

	// Seeds the standard system categories (Salary, Food & Dining, ...)
	// on first run against an empty categories table. Must run after
	// AutoMigrate, which is what creates the table it counts rows in.
	if err := database.SeedCategories(db, logger); err != nil {
		return err
	}

	tokens := auth.NewTokenManager(cfg.JWTSecret, cfg.JWTExpiry)
	requireAuth := middleware.RequireAuth(tokens)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthCheckHandler)

	userHandler := user.NewHandler(db, logger, tokens)
	userHandler.RegisterRoutes(mux, "/api/v1", requireAuth)

	accountHandler := account.NewHandler(db, logger)
	accountHandler.RegisterRoutes(mux, "/api/v1", requireAuth)

	transactionHandler := transaction.NewHandler(db, logger)
	transactionHandler.RegisterRoutes(mux, "/api/v1", requireAuth)

	categoryHandler := category.NewHandler(db, logger)
	categoryHandler.RegisterRoutes(mux, "/api/v1", requireAuth)

	reportHandler := report.NewHandler(db, logger)
	reportHandler.RegisterRoutes(mux, "/api/v1", requireAuth)

	debtHandler := debt.NewHandler(db, logger)
	debtHandler.RegisterRoutes(mux, "/api/v1", requireAuth)

	storeHandler := store.NewHandler(db, logger)
	storeHandler.RegisterRoutes(mux, "/api/v1", requireAuth)

	srv := &http.Server{
		Addr:         ":" + cfg.AppPort,
		Handler:      withCORS(mux, cfg.AllowedOrigins),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("server starting", "port", cfg.AppPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}

	logger.Info("server stopped gracefully")
	return nil
}

func healthCheckHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// withCORS allows cross-origin requests from the configured frontend
// origins. It's needed because the Next.js register page calls this API
// directly from the browser (unlike login, which is proxied through a
// Next.js Route Handler and never leaves the same origin). Preflight
// OPTIONS requests are answered here directly since net/http's ServeMux
// method-based patterns (e.g. "POST /api/v1/register") don't match OPTIONS.
func withCORS(next http.Handler, allowedOrigins []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if slices.Contains(allowedOrigins, origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
