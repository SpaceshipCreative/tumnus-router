package router

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// AcceptResponse is the cascade's quality gate. v1 rejects only responses
// that are unusable on their face: no choices, or an assistant message with
// neither tool calls nor at least minChars runes of text. Tool-call-only
// responses always pass — empty content is normal there.
// ponytail: face-value heuristic; a quality-estimator model call can replace
// this body without touching the escalation plumbing.
func AcceptResponse(resp *schemas.BifrostChatResponse, minChars int) bool {
	if resp == nil || len(resp.Choices) == 0 {
		return false
	}
	// Any passing choice accepts the whole response: chat completions are n=1
	// in practice, and a face-value gate shouldn't escalate an n>1 response
	// that contains at least one usable answer.
	for _, choice := range resp.Choices {
		if choice.ChatNonStreamResponseChoice == nil || choice.ChatNonStreamResponseChoice.Message == nil {
			continue
		}
		msg := choice.ChatNonStreamResponseChoice.Message
		if msg.ChatAssistantMessage != nil && len(msg.ChatAssistantMessage.ToolCalls) > 0 {
			return true
		}
		if msg.Content == nil {
			continue
		}
		var text string
		if msg.Content.ContentStr != nil {
			text = *msg.Content.ContentStr
		} else {
			var parts []string
			for _, block := range msg.Content.ContentBlocks {
				if block.Type == schemas.ChatContentBlockTypeText && block.Text != nil {
					parts = append(parts, *block.Text)
				}
			}
			text = strings.Join(parts, "\n")
		}
		if len([]rune(strings.TrimSpace(text))) >= minChars {
			return true
		}
	}
	return false
}
