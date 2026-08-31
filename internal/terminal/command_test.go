package terminal

import (
	"strings"
	"testing"
)

var testCommands = []Command{
	{Name: "/model", Description: "choose the model to use"},
	{Name: "/help", Description: "show these commands"},
	{Name: "/exit", Description: "end the session"},
}

func TestCommandToken(t *testing.T) {
	tests := map[string]string{
		"/model":            "/model",
		"/model gpt":        "/model",
		"/":                 "/",
		"change the title":  "",
		"":                  "",
		"/model\nsecond":    "",
		" /model":           "",
		"/exit\tabandoned":  "/exit",
		"tell me about /me": "",
	}
	for input, expected := range tests {
		if actual := commandToken(input); actual != expected {
			t.Fatalf("commandToken(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestMatchingCommands(t *testing.T) {
	// A bare slash offers everything, so the commands are discoverable
	// without knowing them up front.
	if matches := matchingCommands(testCommands, "/"); len(matches) != len(testCommands) {
		t.Fatalf("matchingCommands(%q) = %d commands, want %d", "/", len(matches), len(testCommands))
	}
	// Typing narrows it down.
	matches := matchingCommands(testCommands, "/mo")
	if len(matches) != 1 || matches[0].Name != "/model" {
		t.Fatalf("matchingCommands(%q) = %+v, want just /model", "/mo", matches)
	}
	// Ordinary prose offers nothing.
	if matches := matchingCommands(testCommands, "change the title"); matches != nil {
		t.Fatalf("matchingCommands(prose) = %+v, want none", matches)
	}
	// Neither does an unknown command.
	if matches := matchingCommands(testCommands, "/nope"); matches != nil {
		t.Fatalf("matchingCommands(%q) = %+v, want none", "/nope", matches)
	}
}

func TestRenderPromptLineColorsOnlyTheCommand(t *testing.T) {
	rendered := renderPromptLine("/model fast", true)
	if !strings.HasPrefix(rendered, "\x1b[35m/model\x1b[0m") {
		t.Fatalf("rendered = %q, want the command colored", rendered)
	}
	if !strings.HasSuffix(rendered, " fast") {
		t.Fatalf("rendered = %q, want the rest left plain", rendered)
	}
	// Prose and continuation lines are never colored.
	if rendered := renderPromptLine("change the title", true); rendered != "change the title" {
		t.Fatalf("rendered = %q, want it unchanged", rendered)
	}
	if rendered := renderPromptLine("/model", false); rendered != "/model" {
		t.Fatalf("rendered = %q, want continuation lines unchanged", rendered)
	}
}
