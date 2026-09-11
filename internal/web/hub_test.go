package web

import "testing"

func TestHubDeliversPublishedEventsToSubscribers(t *testing.T) {
	h := newHub()
	client, unsubscribe := h.subscribe()
	defer unsubscribe()

	h.publish("phase", map[string]string{"phase": "build"})

	select {
	case evt := <-client:
		if evt.Name != "phase" {
			t.Fatalf("got event %q, want %q", evt.Name, "phase")
		}
		data, ok := evt.Data.(map[string]string)
		if !ok || data["phase"] != "build" {
			t.Fatalf("got data %v, want phase=build", evt.Data)
		}
	default:
		t.Fatal("subscriber did not receive the published event")
	}
}

func TestHubStopsDeliveringAfterUnsubscribe(t *testing.T) {
	h := newHub()
	client, unsubscribe := h.subscribe()
	unsubscribe()

	h.publish("phase", map[string]string{"phase": "load"})

	if _, ok := <-client; ok {
		t.Fatal("an unsubscribed client's channel should be closed, not fed more events")
	}
}

func TestHubDoesNotBlockOnASlowSubscriber(t *testing.T) {
	h := newHub()
	client, unsubscribe := h.subscribe()
	defer unsubscribe()

	// Fill the subscriber's buffer without ever draining it, then publish
	// one more: a stuck tab must not be able to block every other
	// subscriber, or the agent run doing the publishing.
	for i := 0; i < cap(client)+5; i++ {
		h.publish("log", map[string]string{"text": "line"})
	}
}
