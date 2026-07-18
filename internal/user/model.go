package user

import "time"

// User represents a registered account in the system. Password is stored
// as a bcrypt hash and is never serialized back to clients.
type User struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Name         string    `gorm:"type:varchar(150);not null" json:"name"`
	Email        string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"email"`
	PasswordHash string    `gorm:"type:varchar(255);not null" json:"-"`
	Currency     string    `gorm:"type:varchar(3);not null" json:"currency"`
	Timezone     string    `gorm:"type:varchar(64);not null;default:'UTC'" json:"timezone"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// TableName pins the table name explicitly so it does not silently change
// if GORM's pluralization rules change across versions.
func (User) TableName() string {
	return "users"
}
