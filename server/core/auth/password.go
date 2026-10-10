package auth

import "golang.org/x/crypto/bcrypt"

// PasswordCost is the bcrypt work factor. 12 provides strong protection
// against offline GPU cracking dictionaries while remaining efficient for interactive logins.
const PasswordCost = 12

// HashPassword bcrypts a plaintext password with work factor 12.
func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), PasswordCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ComparePassword verifies a plaintext password against a stored hash.
// Returns nil on success.
func ComparePassword(hash, plain string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain))
}
