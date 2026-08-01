package gateway

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"os"
)

const developmentTokenLength = 43

// TokenVerifier retains only a digest of the development token. User lookup,
// revocation, and expiry remain the responsibility of the persistence-backed
// Authenticator used by the running backend.
type TokenVerifier struct {
	digest [sha256.Size]byte
}

func LoadDevelopmentToken(path string) (TokenVerifier, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return TokenVerifier{}, err
	}
	if len(raw) != developmentTokenLength {
		return TokenVerifier{}, fmt.Errorf("development token must be exactly %d bytes", developmentTokenLength)
	}
	if !validToken(string(raw)) {
		return TokenVerifier{}, fmt.Errorf("development token has invalid encoding")
	}
	return TokenVerifier{digest: sha256.Sum256(raw)}, nil
}

func (v TokenVerifier) Verify(token string) bool {
	if !validToken(token) {
		return false
	}
	digest := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(v.digest[:], digest[:]) == 1
}

func validToken(token string) bool {
	if len(token) != developmentTokenLength {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == token
}
