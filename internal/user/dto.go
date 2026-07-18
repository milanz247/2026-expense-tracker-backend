package user

import (
	"errors"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// RegisterRequest is the expected JSON payload for POST /api/v1/register.
type RegisterRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Currency string `json:"currency"`
}

// Validate applies basic business rules to the incoming payload and
// normalizes the email and currency fields. It returns a descriptive
// error suitable for returning directly to the client on failure.
func (r *RegisterRequest) Validate() error {
	r.Name = strings.TrimSpace(r.Name)
	r.Email = strings.TrimSpace(strings.ToLower(r.Email))
	r.Currency = strings.ToUpper(strings.TrimSpace(r.Currency))

	if r.Name == "" {
		return errors.New("name is required")
	}
	if len(r.Name) > 150 {
		return errors.New("name must be at most 150 characters")
	}

	if r.Email == "" {
		return errors.New("email is required")
	}
	if _, err := mail.ParseAddress(r.Email); err != nil {
		return errors.New("email is not a valid address")
	}

	if len(r.Password) < 8 {
		return errors.New("password must be at least 8 characters")
	}

	if !currencyPattern.MatchString(r.Currency) {
		return errors.New("currency must be a 3-letter ISO 4217 code, e.g. USD")
	}

	return nil
}

// RegisterResponse is returned to the client after a successful
// registration. It intentionally excludes the password hash.
type RegisterResponse struct {
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Currency string `json:"currency"`
}

// LoginRequest is the expected JSON payload for POST /api/v1/login.
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Validate normalizes the email and checks that both fields were
// supplied. It deliberately does not enforce the password length rule
// here — that is a registration-time concern, not a login-time one.
func (r *LoginRequest) Validate() error {
	r.Email = strings.TrimSpace(strings.ToLower(r.Email))

	if r.Email == "" {
		return errors.New("email is required")
	}
	if r.Password == "" {
		return errors.New("password is required")
	}

	return nil
}

// AuthUser is the minimal, non-sensitive user info returned alongside a
// JWT on successful login.
type AuthUser struct {
	Name     string `json:"name"`
	Currency string `json:"currency"`
}

// LoginResponse is returned to the client after a successful login.
type LoginResponse struct {
	Token string   `json:"token"`
	User  AuthUser `json:"user"`
}

// ProfileResponse is returned by GET /api/v1/profile and echoed back by
// PUT /api/v1/profile. Email is included for display but is not
// editable through this endpoint.
type ProfileResponse struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Currency string `json:"currency"`
	Timezone string `json:"timezone"`
}

// UpdateProfileRequest is the expected JSON payload for PUT
// /api/v1/profile.
type UpdateProfileRequest struct {
	Name     string `json:"name"`
	Currency string `json:"currency"`
	Timezone string `json:"timezone"`
}

// Validate normalizes and checks the incoming payload. Timezone is
// checked against the IANA tz database (via time.LoadLocation) rather
// than just requiring a non-empty string, so a garbage value can never
// make it into storage and surprise anything that later parses it as a
// real timezone.
func (r *UpdateProfileRequest) Validate() error {
	r.Name = strings.TrimSpace(r.Name)
	r.Currency = strings.ToUpper(strings.TrimSpace(r.Currency))
	r.Timezone = strings.TrimSpace(r.Timezone)

	if r.Name == "" {
		return errors.New("name is required")
	}
	if len(r.Name) > 150 {
		return errors.New("name must be at most 150 characters")
	}

	if !currencyPattern.MatchString(r.Currency) {
		return errors.New("currency must be a 3-letter ISO 4217 code, e.g. USD")
	}

	if r.Timezone == "" {
		return errors.New("timezone is required")
	}
	if _, err := time.LoadLocation(r.Timezone); err != nil {
		return errors.New("timezone must be a valid IANA timezone, e.g. Asia/Colombo")
	}

	return nil
}

// ChangePasswordRequest is the expected JSON payload for POST
// /api/v1/profile/password.
type ChangePasswordRequest struct {
	OldPassword     string `json:"old_password"`
	NewPassword     string `json:"new_password"`
	ConfirmPassword string `json:"confirm_password"`
}

// Validate applies basic shape rules. It deliberately does NOT verify
// OldPassword against the stored hash — that requires a database lookup
// and bcrypt comparison, which belongs in the handler, not here.
func (r *ChangePasswordRequest) Validate() error {
	if r.OldPassword == "" {
		return errors.New("old_password is required")
	}
	if len(r.NewPassword) < 8 {
		return errors.New("new_password must be at least 8 characters")
	}
	if r.NewPassword != r.ConfirmPassword {
		return errors.New("new_password and confirm_password do not match")
	}
	if r.NewPassword == r.OldPassword {
		return errors.New("new_password must be different from old_password")
	}

	return nil
}
