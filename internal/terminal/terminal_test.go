package terminal

import "testing"

func TestMoveSelectionWraps(t *testing.T) {
	tests := []struct {
		current, direction, expected int
	}{
		{0, 1, 1},
		{1, 1, 0},
		{0, -1, 1},
		{1, -1, 0},
	}
	for _, test := range tests {
		if actual := moveSelection(test.current, 2, test.direction); actual != test.expected {
			t.Fatalf("moveSelection(%d, 2, %d) = %d, want %d", test.current, test.direction, actual, test.expected)
		}
	}
}
