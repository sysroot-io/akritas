package web

import (
	"fmt"
	"strings"
)

type DialogueRole string

const (
	RoleSystem    DialogueRole = "system"
	RoleUser      DialogueRole = "user"
	RoleAssistant DialogueRole = "assistant"
)

type DialogueMessage struct {
	Role    DialogueRole
	Content string
}

func ValidateDialogue(messages []DialogueMessage) error {
	if len(messages) == 0 {
		return fmt.Errorf("dialogue contains no messages")
	}
	start := 0
	if messages[0].Role == RoleSystem {
		start = 1
		if len(messages) == 1 {
			return fmt.Errorf("dialogue contains system message but no user message")
		}
	}
	for index, message := range messages {
		if strings.TrimSpace(message.Content) == "" {
			return fmt.Errorf("dialogue message %d has empty content", index)
		}
		if index < start {
			continue
		}
		expected := RoleUser
		if (index-start)%2 == 1 {
			expected = RoleAssistant
		}
		if message.Role != expected {
			return fmt.Errorf("dialogue message %d has role %q, expected %q", index, message.Role, expected)
		}
	}
	return nil
}
