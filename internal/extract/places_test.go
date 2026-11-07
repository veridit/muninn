package extract

import "testing"

func TestParseInvalidPlaceDefaultsWithoutDiscardingMemory(t *testing.T) {
	for _, value := range []string{`null`, `42`, `false`, `{}`, `[]`, `""`, `"unknown"`, `"here"`, `"client"`, `"global"`} {
		text := `{"category":"fact","trust":"high","content":"Synthetic durable placement fact.","place":` + value + `,"place_reason":"Synthetic local explanation."}`
		candidates, rejected := Parse(text)
		if rejected != 0 || len(candidates) != 1 || candidates[0].PlaceReason == "" {
			t.Fatalf("place %s: %+v rejected=%d", value, candidates, rejected)
		}
		if value == `42` || value == `false` || value == `{}` || value == `[]` || value == `null` {
			if candidates[0].Place != "" {
				t.Fatal("malformed place did not default", candidates)
			}
		}
	}
	// Relaxing place metadata must not relax the existing secret/schema gate.
	text := `{"category":"fact","trust":"high","content":"Synthetic durable placement fact.","unexpected":"not allowed"}`
	if candidates, rejected := Parse(text); len(candidates) != 0 || rejected != 1 {
		t.Fatal("unknown fields accepted")
	}
}
