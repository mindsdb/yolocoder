package web

// hubProgress implements agent.Progress by publishing to the SSE hub
// instead of drawing the terminal's animated robot line — the direct web
// counterpart of internal/ui.Session, just a different sink for the same
// two calls the agent loop already makes.
type hubProgress struct {
	hub *hub
}

func (progress hubProgress) Status(text string) {
	progress.hub.publish("status", map[string]string{"text": text})
}

func (progress hubProgress) Log(text string) {
	progress.hub.publish("log", map[string]string{"text": text})
}
