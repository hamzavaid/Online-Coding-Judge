// Package auth provides password hashing and random bearer sessions.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"golang.org/x/crypto/bcrypt"
)

// HashPassword hashes a 12–72 byte password using bcrypt's adaptive work factor.
func HashPassword(password string) (string, error) {
	if len(password) < 12 || len(password) > 72 {
		return "", errors.New("password must be 12–72 bytes")
	}
	b, e := bcrypt.GenerateFromPassword([]byte(password), 12)
	return string(b), e
}

// Verify compares a stored hash without exposing hash internals to callers.
func Verify(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// Token creates 256 bits of entropy; only its digest is persisted.
func Token() (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}

// Digest returns the database representation of a bearer token.
func Digest(token string) string { b := sha256.Sum256([]byte(token)); return hex.EncodeToString(b[:]) }
