package context

import (
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"unicode"
)

type TokenEstimator interface {
	EstimateMessage(schema.Message) int
	EstimateMessages([]schema.Message) int
}

// HeuristicEstimator deliberately overestimates CJK and structured punctuation.
// It is not an official provider tokenizer.
type HeuristicEstimator struct{}

func estimateText(text string) int {
	quarters := 0
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hangul, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
			quarters += 8
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r)):
			quarters += 2
		case r < 128:
			quarters += 4 // JSON punctuation, operators, escapes
		default:
			quarters += 8
		}
	}
	return (quarters + 3) / 4
}
func (HeuristicEstimator) EstimateText(text string) int { return estimateText(text) }

func (HeuristicEstimator) EstimateMessage(m schema.Message) int {
	n := 8 + estimateText(string(m.Role)) + estimateText(m.Content) + estimateText(m.ToolCallID)
	for _, c := range m.ToolCalls {
		n += 12 + estimateText(c.ID) + estimateText(c.Name) + estimateText(string(c.Arguments))
	}
	return n
}
func (e HeuristicEstimator) EstimateMessages(msgs []schema.Message) int {
	n := 0
	for _, m := range msgs {
		n += e.EstimateMessage(m)
	}
	return n
}
