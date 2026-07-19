# Expense Tracker — Backend

REST API for a personal finance app: accounts, categories, transactions, debts, creditors, and PDF/CSV reports. Built with Go on the standard library `net/http` router, GORM, and MySQL.

Part of a three-repo project:
- [expense-tracker-frontend](https://github.com/milanz247/2026-expense-tracker-frontend) — Next.js web client
- [expense-tracker-mobile](https://github.com/milanz247/2026-expense-tracker-mobile) — Android client
- **expense-tracker-backend** (this repo)

## Features

- JWT authentication (`golang-jwt`), password hashing with `golang.org/x/crypto`
- Accounts & wallets with running balances
- Categories, seeded with a standard set (Salary, Food & Dining, …) on first run
- Transactions (income/expense) linked to accounts and categories
- Debts, repayments, creditors, purchases, and settlements
- Report export to CSV and PDF (`gofpdf`)
- CORS configured for a separate frontend origin
- Graceful shutdown on `SIGINT`/`SIGTERM`

## Stack

Go 1.25 · GORM · MySQL · JWT · net/http (no web framework)

## API

All routes are versioned under `/api/v1` and require a bearer token except `GET /healthz`.

| Domain | Handler |
|---|---|
| Auth / users | `internal/user` |
| Accounts | `internal/account` |
| Categories | `internal/category` |
| Transactions | `internal/transaction` |
| Debts & repayments | `internal/debt` |
| Creditors / purchases / settlements | `internal/store` |
| Reports (CSV/PDF) | `internal/report` |

## Getting started

```bash
cp .env.example .env   # set DB credentials, JWT secret, allowed origins
go run ./cmd/api
```

The server auto-migrates the schema and seeds default categories on first run.
