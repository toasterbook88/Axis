package chat

import (
	"reflect"
	"testing"
)

func TestConsolidateMessages(t *testing.T) {
	tests := []struct {
		name string
		in   []Message
		want []Message
	}{
		{
			name: "nil input",
			in:   nil,
			want: nil,
		},
		{
			name: "empty input",
			in:   []Message{},
			want: nil,
		},
		{
			name: "single leading system message unchanged",
			in: []Message{
				{Role: RoleSystem, Content: "you are an assistant"},
				{Role: RoleUser, Content: "hello"},
			},
			want: []Message{
				{Role: RoleSystem, Content: "you are an assistant"},
				{Role: RoleUser, Content: "hello"},
			},
		},
		{
			name: "multiple leading system messages merged with double newline",
			in: []Message{
				{Role: RoleSystem, Content: "base instructions"},
				{Role: RoleSystem, Content: "tools available"},
				{Role: RoleSystem, Content: "cortex memory guide"},
				{Role: RoleUser, Content: "hello"},
			},
			want: []Message{
				{Role: RoleSystem, Content: "base instructions\n\ntools available\n\ncortex memory guide"},
				{Role: RoleUser, Content: "hello"},
			},
		},
		{
			name: "leading system messages with whitespace and empty content skipped",
			in: []Message{
				{Role: RoleSystem, Content: "  base instructions  "},
				{Role: RoleSystem, Content: "   "},
				{Role: RoleSystem, Content: "tools available"},
				{Role: RoleUser, Content: "hello"},
			},
			want: []Message{
				{Role: RoleSystem, Content: "base instructions\n\ntools available"},
				{Role: RoleUser, Content: "hello"},
			},
		},
		{
			name: "no system message at all unchanged",
			in: []Message{
				{Role: RoleUser, Content: "hello"},
				{Role: RoleAssistant, Content: "hi"},
			},
			want: []Message{
				{Role: RoleUser, Content: "hello"},
				{Role: RoleAssistant, Content: "hi"},
			},
		},
		{
			name: "non-leading system message converted to user note",
			in: []Message{
				{Role: RoleSystem, Content: "sys"},
				{Role: RoleUser, Content: "turn 1"},
				{Role: RoleAssistant, Content: "resp 1"},
				{Role: RoleSystem, Content: "context update"},
				{Role: RoleUser, Content: "turn 2"},
			},
			want: []Message{
				{Role: RoleSystem, Content: "sys"},
				{Role: RoleUser, Content: "turn 1"},
				{Role: RoleAssistant, Content: "resp 1"},
				{Role: RoleUser, Content: "[System Note]\ncontext update"},
				{Role: RoleUser, Content: "turn 2"},
			},
		},
		{
			name: "only system messages merges to single message",
			in: []Message{
				{Role: RoleSystem, Content: "sys1"},
				{Role: RoleSystem, Content: "sys2"},
			},
			want: []Message{
				{Role: RoleSystem, Content: "sys1\n\nsys2"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ConsolidateMessages(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ConsolidateMessages() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
