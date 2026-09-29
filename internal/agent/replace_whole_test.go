package agent

import (
	"strings"
	"testing"
)

// The demo's instructions must not carry the sentence they replace: with
// both present, the model wrote whole files as "+" lines under @path.
func TestWholeProjectInstructionsReplaceTheWholeFileRule(t *testing.T) {
	if strings.Contains(wholeProjectChangeInstructions, "Use @+ only for a new file") ||
		!strings.Contains(wholeProjectChangeInstructions, "replace an existing file whole") ||
		!strings.Contains(wholeProjectChangeInstructions, "THIS PROJECT") {
		t.Fatal("the whole-project instructions were not rewritten; has changeInstructions' wording moved?")
	}
}
