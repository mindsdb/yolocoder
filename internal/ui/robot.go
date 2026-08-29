package ui

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

type RobotStatus func(string)

func WithRobot(output *os.File, initialStatus string, task func(RobotStatus)) {
	if !term.IsTerminal(int(output.Fd())) {
		task(func(string) {})
		return
	}

	var mutex sync.RWMutex
	status := initialStatus
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		frames := []string{"[*_*]", "[-_-]", "[*_*]", "[^_^]"}
		ticker := time.NewTicker(140 * time.Millisecond)
		defer ticker.Stop()
		frame := 0
		for {
			mutex.RLock()
			message := status
			mutex.RUnlock()
			drawRobot(output, frames[frame], message)
			select {
			case <-done:
				clearRobot(output)
				return
			case <-ticker.C:
				frame = (frame + 1) % len(frames)
			}
		}
	}()

	task(func(message string) {
		mutex.Lock()
		status = message
		mutex.Unlock()
	})
	close(done)
	<-finished
}

func drawRobot(output io.Writer, frame, status string) {
	fmt.Fprintf(output, "\r\x1b[2K%s %s", frame, status)
}

func clearRobot(output io.Writer) {
	fmt.Fprint(output, "\r\x1b[2K")
}
