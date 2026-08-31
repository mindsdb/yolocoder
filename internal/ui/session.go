package ui

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

var robotFrames = []string{"[*_*]", "[-_-]", "[*_*]", "[^_^]"}

// Session renders a single animated activity line showing what YoloCoder is
// doing right now, with Log writing permanent lines above it. The permanent
// lines are the point: they leave a readable trail of what actually
// happened, instead of one status that overwrites itself and disappears.
//
// On a non-terminal output the animation is skipped and only Log writes.
type Session struct {
	output   *os.File
	animated bool

	mutex  sync.Mutex
	status string
	frame  int
	drawn  bool

	done     chan struct{}
	finished chan struct{}
}

func NewSession(output *os.File) *Session {
	return &Session{output: output, animated: term.IsTerminal(int(output.Fd()))}
}

// Start begins animating the activity line. Stop must follow it.
func (session *Session) Start(status string) {
	session.mutex.Lock()
	session.status = status
	session.mutex.Unlock()
	if !session.animated {
		return
	}
	session.done = make(chan struct{})
	session.finished = make(chan struct{})
	go session.animate()
}

// Status replaces the activity line's message.
func (session *Session) Status(status string) {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	session.status = status
	session.draw()
}

// Log writes a permanent line above the activity line.
func (session *Session) Log(message string) {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	session.clear()
	for _, line := range strings.Split(strings.TrimRight(message, "\n"), "\n") {
		fmt.Fprintln(session.output, line)
	}
	session.draw()
}

// Stop ends the animation and clears the activity line.
func (session *Session) Stop() {
	if session.animated && session.done != nil {
		close(session.done)
		<-session.finished
		session.done = nil
	}
	session.mutex.Lock()
	defer session.mutex.Unlock()
	session.clear()
	session.status = ""
}

func (session *Session) animate() {
	defer close(session.finished)
	ticker := time.NewTicker(140 * time.Millisecond)
	defer ticker.Stop()
	for {
		session.mutex.Lock()
		session.draw()
		session.mutex.Unlock()
		select {
		case <-session.done:
			return
		case <-ticker.C:
			session.mutex.Lock()
			session.frame = (session.frame + 1) % len(robotFrames)
			session.mutex.Unlock()
		}
	}
}

// draw and clear must be called with session.mutex held.
func (session *Session) draw() {
	if !session.animated || session.status == "" {
		return
	}
	drawRobot(session.output, robotFrames[session.frame], session.status)
	session.drawn = true
}

func (session *Session) clear() {
	if !session.animated || !session.drawn {
		return
	}
	clearRobot(session.output)
	session.drawn = false
}
