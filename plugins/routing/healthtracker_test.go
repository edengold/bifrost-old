package routing

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestHealthTrackerRecordAndRecover walks the tracker's contract directly: a
// failure stamps the class and increments the counter, a later success resets
// the counter while keeping the failure facts visible (the half-open signal).
func TestHealthTrackerRecordAndRecover(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Helper()

		tracker := newHealthTracker()
		at := time.Now()

		tracker.recordFailure("LLM Gateway (DevPass)", "key-a", "quota", 429, at)
		snaps := tracker.snapshotForProvider("LLM Gateway (DevPass)")
		if got := len(snaps); got != 1 {
			t.Fatalf("snapshots = %d entries, want 1", got)
		}
		state := snaps["key-a"]
		if state.ConsecutiveFailures != 1 || state.LastFailureClass != "quota" || state.LastStatusCode != 429 {
			t.Errorf("after failure: %+v", state)
		}

		// A second failure accumulates and replaces the class.
		tracker.recordFailure("LLM Gateway (DevPass)", "key-a", "credential", 401, at.Add(5*time.Second))
		state = tracker.snapshotForProvider("LLM Gateway (DevPass)")["key-a"]
		if state.ConsecutiveFailures != 2 || state.LastFailureClass != "credential" || state.LastStatusCode != 401 {
			t.Errorf("after second failure: %+v", state)
		}

		// Success resets the consecutive count but keeps the last-failure facts.
		tracker.recordSuccess("LLM Gateway (DevPass)", "key-a", at.Add(10*time.Second))
		state = tracker.snapshotForProvider("LLM Gateway (DevPass)")["key-a"]
		if state.ConsecutiveFailures != 0 {
			t.Errorf("after success: consecutive = %d, want 0", state.ConsecutiveFailures)
		}
		if state.LastFailureClass != "credential" || state.LastFailureAt == nil {
			t.Errorf("after success: last-failure facts lost: %+v", state)
		}

		// An unrelated provider is untouched by all of the above.
		if snaps := tracker.snapshotForProvider("openrouter"); len(snaps) != 0 {
			t.Errorf("unrelated provider has %d snapshots, want 0", len(snaps))
		}
	})
}

// TestObserveAttemptFromClassifiedError proves the plugin hook feeds the tracker
// from a real BifrostError the way core classifies it: a quota-worded 429 must
// land under ClassifyFailure's "quota" class, not "rate_limit", because that
// ordering (quota before rate limit) is exactly what the exhaustion policy needs.
func TestObserveAttemptFromClassifiedError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Helper()

		plugin := &RoutingPlugin{healthTracker: newHealthTracker()}
		ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
		defer ctx.Cancel()

		status := 429
		err := &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     &status,
			Error: &schemas.ErrorField{
				// Real OpenAI shape: the type carries the quota signal the
				// classifier reads (the message alone would read as a plain
				// rate limit, and that ordering is ClassifyFailure's contract).
				Message: "You exceeded your current quota, please check your plan and billing details.",
			},
		}
		quotaType := "insufficient_quota"
		err.Type = &quotaType
		// Core stamps the serving provider into the error's extra fields before
		// the post hooks run; the test must reproduce that contract.
		err.ExtraFields.Provider = "testprovider"
		ctx.SetValue(schemas.BifrostContextKeyAttemptTrail, []schemas.KeyAttemptRecord{{KeyID: "key-devpass"}})

		plugin.observeAttempt(ctx, nil, err)

		snaps := plugin.healthTracker.snapshotForProvider("testprovider")
		if len(snaps) != 1 {
			t.Fatalf("snapshots = %d, want 1 (provider testprovider)", len(snaps))
		}
		state := snaps["key-devpass"]
		if state.LastFailureClass != "quota" {
			t.Errorf("class = %q, want quota (OpenAI insufficient_quota wording under a 429)", state.LastFailureClass)
		}
	})
}

// TestObserveAttemptIgnoresKeylessEmptyProvider guards the degenerate inputs: a
// missing provider or nil hook inputs must not panic and must not record.
func TestObserveAttemptIgnoresDegenerateInputs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Helper()

		plugin := &RoutingPlugin{healthTracker: newHealthTracker()}
		ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
		defer ctx.Cancel()

		// No trail, no provider: nothing to record, no panic.
		plugin.observeAttempt(ctx, nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "boom"}})
		if snaps := plugin.healthTracker.snapshotForProvider(""); len(snaps) != 0 {
			t.Errorf("recorded under empty provider: %d", len(snaps))
		}

		// Plugin without a tracker: nil-safe no-op.
		bare := &RoutingPlugin{}
		bare.observeAttempt(ctx, nil, nil)
	})
}
