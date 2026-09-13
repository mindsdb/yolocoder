package agent

import "testing"

func TestUsageAdd(t *testing.T) {
	a := Usage{InputTokens: 100, CachedTokens: 20, OutputTokens: 10, TotalTokens: 110}
	b := Usage{InputTokens: 200, CachedTokens: 30, OutputTokens: 50, TotalTokens: 250}
	want := Usage{InputTokens: 300, CachedTokens: 50, OutputTokens: 60, TotalTokens: 360}
	if got := a.add(b); got != want {
		t.Fatalf("add() = %+v, want %+v", got, want)
	}
}

func TestUsageEmpty(t *testing.T) {
	if !(Usage{}).Empty() {
		t.Fatal("a zero Usage should be Empty")
	}
	if (Usage{TotalTokens: 1}).Empty() {
		t.Fatal("a non-zero Usage should not be Empty")
	}
}

func TestUsageSummary(t *testing.T) {
	if got := (Usage{}).Summary(); got != "" {
		t.Fatalf("Summary() of an empty Usage = %q, want empty", got)
	}
	got := Usage{InputTokens: 1234, CachedTokens: 120, OutputTokens: 344, TotalTokens: 1578}.Summary()
	want := "1,578 tokens · 1,234 in (120 cached) · 344 out"
	if got != want {
		t.Fatalf("Summary() = %q, want %q", got, want)
	}
	// No cached-tokens clause at all when the provider didn't report any.
	got = Usage{InputTokens: 890, OutputTokens: 344, TotalTokens: 1234}.Summary()
	want = "1,234 tokens · 890 in · 344 out"
	if got != want {
		t.Fatalf("Summary() without cached tokens = %q, want %q", got, want)
	}
}
