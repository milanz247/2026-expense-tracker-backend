package category

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// Sentinel errors for ValidateType. Exported so every package that lets
// a caller attach a category to something (internal/transaction,
// internal/store, ...) can map the same failure to the same HTTP
// response.
var (
	ErrNotFound     = errors.New("category not found")
	ErrTypeMismatch = errors.New("Invalid category type for this transaction")
)

// ValidateType ensures a client-supplied category is usable for
// something of the given expectedType (e.g. "expense"):
//
//   - It must exist and be visible to the caller — either a global
//     system category (UserID IS NULL) or one they own themselves, the
//     same visibility rule ListCategoriesHandler uses. A category ID
//     that doesn't exist, or belongs to someone else, reads identically
//     as "not found" — never leaking whether an unrelated ID exists.
//   - Its Type must match expectedType — an expense can never be filed
//     under an income category, or vice versa.
func ValidateType(ctx context.Context, db *gorm.DB, userID uint, categoryID uint, expectedType string) error {
	var cat Category
	err := db.WithContext(ctx).
		Where("id = ? AND (user_id IS NULL OR user_id = ?)", categoryID, userID).
		First(&cat).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}

	if cat.Type != expectedType {
		return ErrTypeMismatch
	}

	return nil
}
