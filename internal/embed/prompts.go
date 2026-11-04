package embed

import "strings"

// Prompts are the task prefixes a retrieval model was trained with. Sending
// the wrong ones (or none) silently degrades retrieval: on muninn's own
// title->content benchmark nomic-embed-text gained 3.5 points of recall@1
// from its "search_query: "/"search_document: " prefixes.
type Prompts struct{ Query, Document string }

// PromptsFor returns the prefixes for known model families; unknown models
// get none.
func PromptsFor(model string) Prompts {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "nomic-embed"):
		return Prompts{Query: "search_query: ", Document: "search_document: "}
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
