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
	// Legacy capture rows could share both ID and origin. Repair preserves the
	// original provenance field but gives each recovered row an independent
	// indexed identity. References to the old origin still resolve to the first.
	if strings.HasPrefix(id, "cap_repair_") && strings.HasPrefix(origin, "capture:") {
		return origin + "#" + id
	}
	if origin != "" {
		return origin
	}
	return id
}
