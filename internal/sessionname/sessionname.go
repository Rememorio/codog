package sessionname

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
)

type Options struct {
	Prefix    string
	MaxWords  int
	MaxLength int
}

type Report struct {
	Kind           string   `json:"kind"`
	Action         string   `json:"action"`
	Status         string   `json:"status"`
	SessionID      string   `json:"session_id"`
	SuggestedID    string   `json:"suggested_id"`
	Source         string   `json:"source"`
	SourceText     string   `json:"source_text,omitempty"`
	MessageCount   int      `json:"message_count"`
	CollisionCount int      `json:"collision_count"`
	Renamed        bool     `json:"renamed"`
	OldID          string   `json:"old_id,omitempty"`
	NewID          string   `json:"new_id,omitempty"`
	Path           string   `json:"path,omitempty"`
	Messages       []string `json:"messages,omitempty"`
}

func Suggest(text string, options Options) string {
	options = normalize(options)
	words := wordsFromText(text)
	if len(words) == 0 {
		words = []string{"session"}
	}
	if options.MaxWords > 0 && len(words) > options.MaxWords {
		words = words[:options.MaxWords]
	}
	base := strings.Join(words, "-")
	if options.Prefix != "" {
		base = strings.Trim(options.Prefix, "-") + "-" + base
	}
	if options.MaxLength > 0 && len(base) > options.MaxLength {
		base = strings.Trim(base[:options.MaxLength], "-")
	}
	if base == "" {
		return "session"
	}
	return base
}

func Unique(base string, exists func(string) (bool, error)) (string, int, error) {
	base = strings.Trim(strings.TrimSpace(base), "-")
	if base == "" {
		base = "session"
	}
	for index := 0; index < 1000; index++ {
		candidate := base
		if index > 0 {
			candidate = base + "-" + strconv.Itoa(index+1)
		}
		ok, err := exists(candidate)
		if err != nil {
			return "", 0, err
		}
		if !ok {
			return candidate, index, nil
		}
	}
	return "", 0, fmt.Errorf("could not find available session name for %q", base)
}

func RenderText(out io.Writer, report Report) {
	fmt.Fprintln(out, "Session Name")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Session          %s\n", report.SessionID)
	fmt.Fprintf(out, "  Suggested        %s\n", report.SuggestedID)
	fmt.Fprintf(out, "  Source           %s\n", report.Source)
	fmt.Fprintf(out, "  Messages         %d\n", report.MessageCount)
	if report.CollisionCount > 0 {
		fmt.Fprintf(out, "  Collisions       %d\n", report.CollisionCount)
	}
	if report.Renamed {
		fmt.Fprintf(out, "  Renamed          %s -> %s\n", report.OldID, report.NewID)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Path             %s\n", report.Path)
	}
	for _, message := range report.Messages {
		fmt.Fprintf(out, "  Message          %s\n", message)
	}
}

func RenderJSON(out io.Writer, report Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(data))
	return err
}

func normalize(options Options) Options {
	if options.MaxWords <= 0 {
		options.MaxWords = 7
	}
	if options.MaxLength <= 0 {
		options.MaxLength = 60
	}
	options.Prefix = sanitizePrefix(options.Prefix)
	return options
}

func sanitizePrefix(prefix string) string {
	words := wordsFromText(prefix)
	if len(words) == 0 {
		return ""
	}
	if len(words) > 3 {
		words = words[:3]
	}
	value := strings.Join(words, "-")
	if len(value) > 24 {
		value = strings.Trim(value[:24], "-")
	}
	return value
}

func wordsFromText(text string) []string {
	text = strings.ToLower(strings.TrimSpace(text))
	var b strings.Builder
	lastDash := false
	for _, r := range text {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		case r == '_' || r == '-' || unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r):
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	raw := strings.FieldsFunc(strings.Trim(b.String(), "-"), func(r rune) bool { return r == '-' })
	words := make([]string, 0, len(raw))
	for _, word := range raw {
		word = strings.Trim(word, "-")
		if word == "" || stopWord(word) {
			continue
		}
		words = append(words, word)
	}
	return words
}

func stopWord(word string) bool {
	switch word {
	case "a", "an", "and", "are", "as", "at", "be", "by", "for", "from", "in", "is", "it", "of", "on", "or", "the", "to", "with":
		return true
	default:
		return false
	}
}
