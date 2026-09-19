package models

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"math/big"
)

// crockford is the Base32 alphabet used for organization codes (no I, L, O, U).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewToken returns 32 cryptographically random bytes as unpadded base64url.
func NewToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashToken returns the SHA-256 digest used to store a token.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// NewOrgCode returns a random eight-character Crockford Base32 code.
func NewOrgCode() string {
	out := make([]byte, 8)
	max := big.NewInt(int64(len(crockford)))
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic("crypto/rand unavailable: " + err.Error())
		}
		out[i] = crockford[n.Int64()]
	}
	return string(out)
}

// HashHex is HashToken as lowercase hex, for use with decode(?, 'hex') in raw SQL.
// GORM expands a []byte argument in Raw() into one bind variable per byte.
func HashHex(token string) string { return hex.EncodeToString(HashToken(token)) }
