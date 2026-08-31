package ui

import (
	"fmt"
	"io"
	"os"
)

type RobotStatus func(string)

// WithRobot animates the robot next to initialStatus for the duration of
// task, which may replace the message at any time.
func WithRobot(output *os.File, initialStatus string, task func(RobotStatus)) {
	session := NewSession(output)
	session.Start(initialStatus)
	defer session.Stop()
	task(session.Status)
}

func drawRobot(output io.Writer, frame, status string) {
	fmt.Fprintf(output, "\r\x1b[2K%s %s", frame, status)
}

func clearRobot(output io.Writer) {
	fmt.Fprint(output, "\r\x1b[2K")
}
