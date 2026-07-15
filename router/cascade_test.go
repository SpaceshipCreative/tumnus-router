package router

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func chatResp(msg *schemas.ChatMessage) *schemas.BifrostChatResponse {
	return &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{{
			ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{Message: msg},
		}},
	}
}

func TestAcceptResponse(t *testing.T) {
	text := func(s string) *schemas.ChatMessage {
		return &schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{ContentStr: &s},
		}
	}
	toolCall := &schemas.ChatMessage{
		Role: schemas.ChatMessageRoleAssistant,
		ChatAssistantMessage: &schemas.ChatAssistantMessage{
			ToolCalls: []schemas.ChatAssistantMessageToolCall{{}},
		},
	}
	cases := []struct {
		name string
		resp *schemas.BifrostChatResponse
		want bool
	}{
		{"nil response", nil, false},
		{"no choices", &schemas.BifrostChatResponse{}, false},
		{"nil message", chatResp(nil), false},
		{"empty text", chatResp(text("")), false},
		{"whitespace only", chatResp(text("  \n\t ")), false},
		{"real text", chatResp(text("42")), true},
		{"tool call with no text", chatResp(toolCall), true},
	}
	for _, tc := range cases {
		if got := AcceptResponse(tc.resp, 1); got != tc.want {
			t.Errorf("%s: got %t, want %t", tc.name, got, tc.want)
		}
	}
	// min chars is rune-counted and enforced.
	if AcceptResponse(chatResp(text("hi")), 3) {
		t.Error("2 chars must fail a min of 3")
	}
	if !AcceptResponse(chatResp(text("héé")), 3) {
		t.Error("3 runes must pass a min of 3")
	}
}
