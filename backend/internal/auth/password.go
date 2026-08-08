// Package auth handles password hashing and verification (bcrypt) and JSON Web
// Token signing/parsing (HS256) for the Webshell backend.
package auth

import "golang.org/x/crypto/bcrypt"

// bcryptCost is the bcrypt cost factor used when hashing new passwords. Cost 10
// is the bcrypt default and provides a reasonable work factor without making
// login prohibitively slow.
const bcryptCost = 10

// HashPassword returns the bcrypt hash of pw using cost 10. The returned hash
// is safe to persist verbatim.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// CheckPassword reports whether pw matches the given bcrypt hash. It returns
// false on any mismatch or error (e.g. malformed hash); callers should treat a
// false result as "not authenticated" and never rely on the error to distinguish
// hash-format problems from a plain wrong password.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}