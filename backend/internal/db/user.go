package db

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type AuthSource string

const (
	AuthSourcePassword AuthSource = "password"
	AuthSourceOIDC     AuthSource = "oidc"
	AuthSourceSSO      AuthSource = "sso"
	AuthSourceDev      AuthSource = "dev"
	AuthSourceUnknown  AuthSource = "unknown"
)

type User struct {
	ID           uint       `gorm:"primaryKey;autoIncrement"`
	UserName     string     `gorm:"uniqueIndex;size:100;not null"`
	Email        string     `gorm:"size:255;not null"`
	PasswordHash string     `gorm:"size:255;not null"`
	IsActive     bool       `gorm:"default:true"`
	AuthSource   AuthSource `gorm:"size:20;not null;default:'unknown'"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// FindOrCreate looks up a user by username; creates one if absent.
// For SSO/OIDC users pass AuthSourceSSO/AuthSourceOIDC — a random bcrypt hash is stored.
func FindOrCreate(db *gorm.DB, userName, email string, src AuthSource) (*User, error) {
	var u User
	res := db.Where("user_name = ?", userName).First(&u)
	if res.Error == nil {
		changed := false
		if u.Email != email {
			u.Email = email
			changed = true
		}
		if u.AuthSource == AuthSourceUnknown {
			u.AuthSource = src
			changed = true
		}
		if changed {
			if err := db.Save(&u).Error; err != nil {
				return nil, err
			}
		}
		return &u, nil
	}
	if !errors.Is(res.Error, gorm.ErrRecordNotFound) {
		return nil, res.Error
	}

	randHash, err := bcrypt.GenerateFromPassword([]byte(uuid.New().String()), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	u = User{
		UserName:     userName,
		Email:        email,
		PasswordHash: string(randHash),
		IsActive:     true,
		AuthSource:   src,
	}
	if err := db.Create(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// FindByCredentials looks up a user by username, verifies their password,
// and checks the account is active. Returns nil (no error) on bad credentials
// or inactive account — callers should return 401 in that case.
func FindByCredentials(db *gorm.DB, userName, password string) (*User, error) {
	var u User
	if err := db.Where("user_name = ?", userName).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if !u.IsActive {
		return nil, nil
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, nil
	}
	return &u, nil
}

// HashAPIKey returns the SHA-256 hex digest used to store/verify API keys.
func HashAPIKey(key string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
}
