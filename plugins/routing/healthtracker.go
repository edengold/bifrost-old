package routing

import (
	"strings"
	"sync"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/routing/rules"
)

// healthTracker remembers, across requests, how the most recent attempts against
// each provider key ended. Routing rules read the state through CEL variables
// (last_failure_class, last_status_code, failure_consecutive, seconds_since_last_failure
// for the current provider, and the key_health map for per-key detail), so the
// trip/hold policy lives in the rules, not here: the tracker only bookkeeps what
// the attempts already recorded on the attempt trail.
//
// State is per-process and in memory. A Bifrost restart starts empty, which is
// the safe direction: the first request re-probes every key as if healthy.
type healthTracker struct {
	mu     sync.Mutex
	byKey  map[string]rules.KeyHealthSnapshot
	keysOf map[string]map[string]struct{} // provider -> set of its tracker keys
}

func newHealthTracker() *healthTracker {
	return &healthTracker{
		byKey:  make(map[string]rules.KeyHealthSnapshot),
		keysOf: make(map[string]map[string]struct{}),
	}
}

// trackerKey is the per-key state key: "provider" alone for a keyless provider
// ("provider|keyID" when the attempt named a key). Provider strings have no "|".
func trackerKey(provider, keyID string) string {
	if keyID == "" {
		return provider
	}
	return provider + "|" + keyID
}

func (t *healthTracker) touch(provider, key string) {
	if _, ok := t.keysOf[provider]; !ok {
		t.keysOf[provider] = make(map[string]struct{})
	}
	t.keysOf[provider][key] = struct{}{}
}

// recordSuccess resets a key's consecutive-failure count and stamps the recovery.
// The last-failure facts are kept on purpose: a rule can express "the key was
// quota-blocked AND has served since" (failure_consecutive == 0 alongside a fresh
// last_failure_class), which is the half-open signal.
func (t *healthTracker) recordSuccess(provider, keyID string, at time.Time) {
	if provider == "" {
		return
	}
	key := trackerKey(provider, keyID)
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.byKey[key]
	t.touch(provider, key)
	state.LastSuccessAt = at
	if state.LastFailureAt == nil || at.After(*state.LastFailureAt) {
		state.ConsecutiveFailures = 0
	}
	t.byKey[key] = state
}

// recordFailure stamps one failed attempt. It replaces the last-failure record
// outright: rules read the most recent failure, and a stale block that predates a
// newer transient failure must not leak into a "quota" predicate.
func (t *healthTracker) recordFailure(provider, keyID string, failureClass string, statusCode int, at time.Time, errorMessage string) {
	if provider == "" {
		return
	}
	if len(errorMessage) > 512 {
		errorMessage = errorMessage[:512]
	}
	key := trackerKey(provider, keyID)
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.byKey[key]
	t.touch(provider, key)
	state.ConsecutiveFailures++
	state.LastFailureClass = failureClass
	state.LastStatusCode = statusCode
	state.LastErrorMessage = errorMessage
	state.LastFailureAt = &at
	t.byKey[key] = state
}

// snapshotAll returns every provider's per-key snapshots, keyed provider →
// keyID → snapshot. Feeds the rules package's provider_health table.
func (t *healthTracker) snapshotAll() map[string]map[string]rules.KeyHealthSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]map[string]rules.KeyHealthSnapshot, len(t.keysOf))
	for provider, keys := range t.keysOf {
		perProvider := make(map[string]rules.KeyHealthSnapshot, len(keys))
		for key := range keys {
			keyID := ""
			if i := strings.IndexByte(key, '|'); i >= 0 {
				keyID = key[i+1:]
			}
			perProvider[keyID] = t.byKey[key]
		}
		out[provider] = perProvider
	}
	return out
}

// snapshotForProvider returns the per-key snapshots of one provider, keyed by
// keyID ("" entry for keyless). Aggregation into the provider-level scalars is
// the rules package's job, so the tracker stays a pure record.
func (t *healthTracker) snapshotForProvider(provider string) map[string]rules.KeyHealthSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]rules.KeyHealthSnapshot, len(t.keysOf[provider]))
	for key := range t.keysOf[provider] {
		keyID := ""
		if i := strings.IndexByte(key, '|'); i >= 0 {
			keyID = key[i+1:]
		}
		out[keyID] = t.byKey[key]
	}
	return out
}

// observeAttempt feeds one finished request outcome into the tracker. The caller
// resolves which provider served (GetResponseFields works for both a response and
// an error); the attempt trail's last record names the key that served or failed.
func (p *RoutingPlugin) observeAttempt(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) {
	if ctx == nil || p.healthTracker == nil {
		return
	}
	now := time.Now()
	// GetResponseFields returns (requestType, provider, originalModel, resolvedModel).
	_, provider, _, _ := bifrost.GetResponseFields(resp, bifrostErr)
	if provider == "" {
		return
	}
	keyID := lastAttemptKeyID(ctx)
	if bifrostErr != nil {
		class := bifrost.ClassifyFailure(bifrostErr)
		status := 0
		if bifrostErr.StatusCode != nil {
			status = *bifrostErr.StatusCode
		}
		errorMessage := ""
		if bifrostErr.Error != nil {
			errorMessage = bifrostErr.Error.Message
		}
		p.healthTracker.recordFailure(string(provider), keyID, string(class), status, now, errorMessage)
		return
	}
	if resp == nil {
		return
	}
	// Streams fire the post hook per chunk; only the final one counts as the
	// outcome, so per-chunk successes do not mask an error already recorded
	// mid-stream.
	extraFields := resp.GetExtraFields()
	if bifrost.IsStreamRequestType(extraFields.RequestType) && !bifrost.IsFinalChunk(ctx) {
		return
	}
	p.healthTracker.recordSuccess(string(provider), keyID, now)
}

// lastAttemptKeyID reads the key the final attempt used. The trail is cleared for
// fallback re-runs and rebuilt per attempt, so the last record is always the
// current one.
func lastAttemptKeyID(ctx *schemas.BifrostContext) string {
	trail, ok := ctx.Value(schemas.BifrostContextKeyAttemptTrail).([]schemas.KeyAttemptRecord)
	if !ok || len(trail) == 0 {
		return ""
	}
	return trail[len(trail)-1].KeyID
}
