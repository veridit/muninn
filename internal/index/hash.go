// Package index holds the content-addressed identity rules.
//
// Design (from seid search, TASK-1105): every input that determines an
// embedding row lives in this one place. A unit's identity is the SHA-256 of
// the exact text that is embedded; the pipeline hash covers the embedding
// recipe. Changing the recipe invalidates exactly the vectors whose identity
// changed; unrelated changes never re-embed anything.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// SchemaVersion bumps when the representation of persisted vectors changes
// without a corresponding change to the inputs below.
const SchemaVersion = 1

// MaxEmbedBytes caps embedding input (seid search: 2000-byte truncation).
const MaxEmbedBytes = 2000

// RecipeVersion bumps whenever EmbeddingText changes behaviour.
const RecipeVersion = 1

// EmbeddingText is the exact text sent to the embedder: title, newline, body,
// truncated at a UTF-8 boundary.
func EmbeddingText(title, body string) string {
	full := body
	if title != "" {
		full = title + "\n" + body
	}
	if len(full) <= MaxEmbedBytes {
		return full
	}
	cut := MaxEmbedBytes
	for cut > 0 && full[cut]&0xC0 == 0x80 { // do not split a rune
		cut--
	}
	return full[:cut]
}

// ContentHash identifies a unit by its embedded text.
func ContentHash(embeddingText string) string {
	sum := sha256.Sum256([]byte(embeddingText))
	return hex.EncodeToString(sum[:])
}

// PipelineHash identifies the embedding recipe for a model.
func PipelineHash(model string) string {
	h := sha256.New()
	h.Write([]byte("muninn-embed\x00schema=" + strconv.Itoa(SchemaVersion) +
		"\x00recipe=" + strconv.Itoa(RecipeVersion) +
		"\x00max=" + strconv.Itoa(MaxEmbedBytes) + "\x00model=" + model))
	return hex.EncodeToString(h.Sum(nil))
}
