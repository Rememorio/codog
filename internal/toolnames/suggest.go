// Package toolnames provides shared helpers for canonical tool-name handling.
package toolnames

import (
	"sort"
	"strings"
)

// Suggestions returns likely canonical tool names for a misspelled query.
func Suggestions(query string, candidates []string, limit int) []string {
	key := suggestionKey(query)
	if key == "" || limit <= 0 {
		return nil
	}
	type scoredSuggestion struct {
		Name  string
		Score int
	}
	scored := make([]scoredSuggestion, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		candidateKey := suggestionKey(candidate)
		if candidate == "" || candidateKey == "" {
			continue
		}
		if candidateKey == key && strings.EqualFold(candidate, strings.TrimSpace(query)) {
			continue
		}
		score := suggestionDistance(key, candidateKey)
		if strings.Contains(candidateKey, key) || strings.Contains(key, candidateKey) {
			score--
		}
		if score <= suggestionThreshold(key, candidateKey) {
			scored = append(scored, scoredSuggestion{Name: candidate, Score: score})
		}
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score < scored[j].Score
		}
		return scored[i].Name < scored[j].Name
	})
	out := make([]string, 0, min(limit, len(scored)))
	seen := map[string]struct{}{}
	for _, candidate := range scored {
		if _, ok := seen[candidate.Name]; ok {
			continue
		}
		seen[candidate.Name] = struct{}{}
		out = append(out, candidate.Name)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func suggestionKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func suggestionThreshold(a string, b string) int {
	longest := max(len(a), len(b))
	switch {
	case longest <= 4:
		return 1
	case longest <= 10:
		return 2
	default:
		return 3
	}
}

func suggestionDistance(a string, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return len(b)
	}
	if b == "" {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			curr[j] = min(
				prev[j]+1,
				curr[j-1]+1,
				prev[j-1]+cost,
			)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}
