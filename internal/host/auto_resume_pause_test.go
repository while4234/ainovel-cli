package host

import "testing"

func TestManualPauseCancelsAutomaticResumeInFlight(t *testing.T) {
	h := &Host{
		lifecycle:          lifecycleIdle,
		autoResumeInFlight: true,
	}

	_, active := h.pauseActiveRun()
	if !active {
		t.Fatal("automatic resume in flight should be treated as active")
	}
	if h.lifecycle != lifecyclePaused {
		t.Fatalf("lifecycle = %q, want %q", h.lifecycle, lifecyclePaused)
	}
}

func TestAutomaticResumeCannotOverwriteManualPause(t *testing.T) {
	h := &Host{
		lifecycle:          lifecyclePaused,
		autoResumeInFlight: true,
	}

	if h.publishResumedLifecycle(true) {
		t.Fatal("automatic resume should be canceled after manual pause")
	}
	if h.lifecycle != lifecyclePaused {
		t.Fatalf("lifecycle = %q, want %q", h.lifecycle, lifecyclePaused)
	}
}
