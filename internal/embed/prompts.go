package embed

import "strings"

// Prompts are the task prefixes a retrieval model was trained with. Some
// models need them; for others they trade one property for another, so each
// entry here is a measured decision, not a default.
//
// nomic-embed-text gets none: on 200 real title->content queries over 2201
// memories its "search_query: "/"search_document: " prefixes raised
// recall@1 from 0.705 to 0.745, but recall@10 barely moved (0.950 -> 0.955)
// and they lifted chit-chat ("thanks", "continue") as much as true matches.
// Automatic recall injects on an absolute cosine gate, and at zero injected
// chit-chat (0/14 probes) the gate kept 88% of true matches without
// prefixes (T=0.66) but only 80% with them (T=0.74).
type Prompts struct{ Query, Document string }

// PromptsFor returns the prefixes for known model families; unknown models
// get none.
func PromptsFor(model string) Prompts {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "e5"):
		return Prompts{Query: "query: ", Document: "passage: "}
	case strings.Contains(m, "bge-") && strings.Contains(m, "en"),
		strings.Contains(m, "arctic-embed"):
		return Prompts{Query: "Represent this sentence for searching relevant passages: "}
	}
	return Prompts{}
}

// Query embeds one search query with the model's query prefix.
func Query(e Embedder, text string) string { return PromptsFor(e.Model()).Query + text }

// Documents prefixes texts for indexing.
func Documents(e Embedder, texts []string) []string {
	p := PromptsFor(e.Model()).Document
	if p == "" {
		return texts
	}
	out := make([]string, len(texts))
	for i, t := range texts {
		out[i] = p + t
	}
	return out
}
