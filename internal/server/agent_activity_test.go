package server

import (
	"strings"
	"testing"

	"mcpx/internal/envelope"
)

func TestEmbeddedActivityUpdatesUseCanonicalFieldOrder(t *testing.T) {
	updates, err := embeddedActivityUpdates(envelope.ActivityInput{
		Status:     "status",
		Next:       "next",
		Conclusion: "conclusion",
		Evidence:   "evidence",
		Hypothesis: "hypothesis",
		Intent:     "intent",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"intent", "hypothesis", "evidence", "conclusion", "next", "status"}
	if len(updates) != len(want) {
		t.Fatalf("updates=%+v", updates)
	}
	for i, kind := range want {
		if updates[i].Kind != kind || updates[i].Summary != kind {
			t.Fatalf("update[%d]=%+v want kind=%s", i, updates[i], kind)
		}
	}
}

func TestEmbeddedActivityUpdatesIgnoreBlankFields(t *testing.T) {
	updates, err := embeddedActivityUpdates(envelope.ActivityInput{
		Intent:   "  inspect implementation  ",
		Evidence: "   ",
		Next:     "read next file",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 2 || updates[0].Kind != "intent" || updates[0].Summary != "inspect implementation" || updates[1].Kind != "next" {
		t.Fatalf("updates=%+v", updates)
	}
}

func TestEmbeddedActivityUpdatesRejectOversizedSummary(t *testing.T) {
	_, err := embeddedActivityUpdates(envelope.ActivityInput{Evidence: strings.Repeat("x", envelope.MaxActivityBytes+1)})
	if err == nil || !strings.Contains(err.Error(), "activity.evidence exceeds") {
		t.Fatalf("err=%v", err)
	}
}

func TestEmbeddedActivityUpdatesAcceptsLongEvidenceWithinActivityLimit(t *testing.T) {
	updates, err := embeddedActivityUpdates(envelope.ActivityInput{Evidence: strings.Repeat("x", envelope.MaxIntentBytes+1)})
	if err != nil {
		t.Fatalf("activity evidence should not use the shorter purpose limit: %v", err)
	}
	if len(updates) != 1 || updates[0].Kind != "evidence" {
		t.Fatalf("updates=%+v", updates)
	}
}
