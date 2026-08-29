package ui

import (
	"bytes"
	"testing"
)

func TestDrawRobot(t *testing.T) {
	var output bytes.Buffer
	drawRobot(&output, "[*_*]", "Updating YoloCoder...")
	if got := output.String(); got != "\r\x1b[2K[*_*] Updating YoloCoder..." {
		t.Fatalf("drawRobot() = %q", got)
	}
}
