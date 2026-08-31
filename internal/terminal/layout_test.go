package terminal

import "testing"

func TestWrappedRows(t *testing.T) {
	const width = 80
	tests := []struct {
		runes    int
		expected int
	}{
		{0, 1},
		{1, 1},
		{width - editorPrefixWidth - 1, 1},
		// Exactly filling the row still occupies one row, not two.
		{width - editorPrefixWidth, 1},
		{width - editorPrefixWidth + 1, 2},
		{2*width - editorPrefixWidth, 2},
		{2*width - editorPrefixWidth + 1, 3},
	}
	for _, test := range tests {
		if actual := wrappedRows(test.runes, width); actual != test.expected {
			t.Fatalf("wrappedRows(%d, %d) = %d, want %d", test.runes, width, actual, test.expected)
		}
	}
}
