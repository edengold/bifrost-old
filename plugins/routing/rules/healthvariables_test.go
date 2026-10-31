package rules

import (
	"testing"
	"testing/synctest"
	"time"
)

// TestApplyProviderHealthVariablesDefaults covers the no-tracker and no-record
// shapes: every health variable must carry its neutral value, the same values an
// operator's rule sees on a fresh process where nothing has failed yet.
func TestApplyProviderHealthVariablesDefaults(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Helper()

		// Nil health entirely: tracker not wired.
		vars := map[string]any{}
		applyProviderHealthVariables(vars, nil, nil)
		if got := vars["last_failure_class"]; got != "" {
			t.Errorf("last_failure_class = %v, want empty", got)
		}
		if got := vars["key_healthy"]; got != true {
			t.Errorf("key_healthy = %v, want true with no data", got)
		}
		if got := vars["seconds_since_last_failure"]; got != -1.0 {
			t.Errorf("seconds_since_last_failure = %v, want -1", got)
		}

		// Tracker wired, provider unknown.
		vars = map[string]any{}
		applyProviderHealthVariables(vars, map[string]KeyHealthSnapshot{}, nil)
		if got := vars["key_healthy"]; got != true {
			t.Errorf("key_healthy = %v, want true with empty snapshot", got)
		}
	})
}

// TestApplyProviderHealthVariablesScalars walks the subject selection and value
// plumbing with one unhealthy and one healthy key.
func TestApplyProviderHealthVariablesScalars(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Helper()

		now := time.Now()
		failedAt := now.Add(-30 * time.Second)
		health := map[string]KeyHealthSnapshot{
			"key-healthy": {LastSuccessAt: now.Add(-2 * time.Second)},
			"key-quota": {
				ConsecutiveFailures: 3,
				LastFailureClass:    "quota",
				LastStatusCode:      429,
				LastFailureAt:       &failedAt,
			},
		}

		vars := map[string]any{}
		applyProviderHealthVariables(vars, health, nil)

		// The quota key failed most recently; the scalars must describe it.
		if got := vars["last_failure_class"]; got != "quota" {
			t.Errorf("last_failure_class = %v, want quota", got)
		}
		if got := vars["last_status_code"]; got != 429 {
			t.Errorf("last_status_code = %v, want 429", got)
		}
		if got := vars["failure_consecutive"]; got != 3 {
			t.Errorf("failure_consecutive = %v, want 3", got)
		}
		if got := vars["seconds_since_last_failure"]; got != 30.0 {
			t.Errorf("seconds_since_last_failure = %v, want 30", got)
		}
		if got := vars["key_healthy"]; got != false {
			t.Errorf("key_healthy = %v, want false (persistent quota)", got)
		}

		// The per-key map must carry both entries with usable numbers.
		keyHealth, ok := vars["key_health"].(map[string]any)
		if !ok || len(keyHealth) != 2 {
			t.Fatalf("key_health = %#v, want 2 entries", vars["key_health"])
		}
		healthy := keyHealth["key-healthy"].(map[string]any)
		if healthy["consecutive_failures"] != 0 {
			t.Errorf("healthy key consecutive_failures = %v, want 0", healthy["consecutive_failures"])
		}
	})
}

// TestIsHealthyClassPolicy pins the class policy: rate_limit and transient
// self-heal inside a request, so they never make a key unhealthy; quota,
// credential and friends persist until a success proves otherwise.
func TestIsHealthyClassPolicy(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		snap KeyHealthSnapshot
		want bool
	}{
		{"no failure", KeyHealthSnapshot{}, true},
		{"rate limit persists", KeyHealthSnapshot{LastFailureClass: "rate_limit", LastFailureAt: &now}, true},
		{"transient persists", KeyHealthSnapshot{LastFailureClass: "transient", LastFailureAt: &now}, true},
		{"quota blocks", KeyHealthSnapshot{LastFailureClass: "quota", LastFailureAt: &now}, false},
		{"credential blocks", KeyHealthSnapshot{LastFailureClass: "credential", LastFailureAt: &now}, false},
		{"unknown blocks", KeyHealthSnapshot{LastFailureClass: "unknown_failure", LastFailureAt: &now}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isHealthy(tc.snap, now); got != tc.want {
				t.Errorf("isHealthy(%+v) = %v, want %v", tc.snap, got, tc.want)
			}
		})
	}

	// Success after failure flips every class back to healthy.
	failedAt := now.Add(-time.Minute)
	snap := KeyHealthSnapshot{LastFailureClass: "quota", LastFailureAt: &failedAt, LastSuccessAt: now}
	if got := isHealthy(snap, now); got != true {
		t.Errorf("isHealthy(after success) = %v, want true", got)
	}
}
