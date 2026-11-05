// Package shortid defines portable memory identifiers.
package shortid

import (
	"crypto/sha256"
	"encoding/base32"
	"strings"
)

const Alphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// Full returns the deterministic Crockford encoding of an origin identity.
func Full(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return strings.ToLower(base32.NewEncoding(Alphabet).WithPadding(base32.NoPadding).EncodeToString(sum[:]))
}

// Identity retains upstream identity independently of display ids.
func Identity(id, origin string) string {
	if origin != "" {
		return origin
	}
	return id
}
