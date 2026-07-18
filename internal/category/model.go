package category

import (
	"time"

	"github.com/milan/expense-tracker/backend/internal/user"
)

// Category groups transactions for reporting/filtering (e.g. "Food &
// Dining", "Salary"). UserID is nullable: a nil UserID marks a global,
// system-seeded category visible to every user (see
// pkg/database/seed.go), while a non-nil UserID marks a category a
// specific user created for themselves — editable and deletable only by
// that user, never by anyone else and never a system category.
type Category struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// UserID is a pointer so it can be genuinely NULL in the database
	// (Go's zero value for uint, 0, would otherwise be indistinguishable
	// from "owned by user 0").
	UserID *uint `gorm:"index" json:"user_id"`
	// User is a belongs-to association purely so GORM's AutoMigrate emits
	// a real foreign key constraint on UserID; the handler never
	// populates or serializes it. Deleting a user cascades their own
	// custom categories, but a nil UserID (a global category) is never
	// touched by that cascade.
	User user.User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
	Name string    `gorm:"type:varchar(100);not null" json:"name"`
	Type string    `gorm:"type:varchar(20);not null;index" json:"type"`
	// Color is a Tailwind color token (e.g. "emerald"), not raw CSS or an
	// arbitrary hex string — validated server-side against a closed
	// allowlist in dto.go and mapped to fixed classes on the frontend
	// (see lib/categories.ts's CATEGORY_COLORS). Keeps the "Color Preset
	// Picker" honest: only ever a small, reviewed palette reaches the UI.
	Color string `gorm:"type:varchar(30);not null" json:"color"`
	// Icon is a Lucide icon name (e.g. "HeartPulse"), similarly validated
	// against a closed allowlist and resolved against a fixed icon map
	// on the frontend (lib/categories.ts's CATEGORY_ICONS).
	Icon string `gorm:"type:varchar(50);not null" json:"icon"`
	// IsSystem marks a category the application itself creates and
	// maintains (see pkg/database/seed.go's SeedCategories) rather than
	// one a user typed into the "Add Category" form. Every seeded
	// category — both the ordinary ones a user can file their own
	// transactions under (Salary, Food & Dining, ...) and the five
	// auto-assign-only ones (Transfer, Store Tab Payment, Debt Issued,
	// Debt Borrowed, Repayment — see internal/category/system.go) — sets
	// this true; every user-created category leaves it at the default
	// false. It always coincides with UserID being nil (only the seeder
	// ever creates a UserID-nil row, and CreateCategoryHandler never
	// accepts this field from a request body), but is kept as its own
	// explicit column rather than derived, so a query can filter on it
	// directly — see LookupSystemID, which relies on it to find the
	// exact auto-assign category by name without caring about UserID.
	IsSystem  bool      `gorm:"not null;default:false;index" json:"is_system"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the table name explicitly so it does not silently
// change if GORM's pluralization rules change across versions.
func (Category) TableName() string {
	return "categories"
}
