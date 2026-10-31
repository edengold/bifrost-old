package rules

import (
	"time"
)

// KeyHealthSnapshot is what the routing plugin remembers about the most recent
// attempts against one provider key. Values are plain data so the rules package
// can aggregate and expose them to CEL without knowing how they were produced.
type KeyHealthSnapshot struct {
	// ConsecutiveFailures counts failures since the last success (0 after a
	// success). It never decays on its own: only a served attempt resets it.
	ConsecutiveFailures int
	// LastFailureClass is the ClassifyFailure class of the most recent failure
	// ("quota", "credential", "rate_limit", "transient", "timeout", ...), empty
	// when the key has never failed.
	LastFailureClass string
	// LastStatusCode is the HTTP status of the most recent failure, 0 when none.
	LastStatusCode int
	// LastFailureAt is when the most recent failure was recorded; nil when none.
	LastFailureAt *time.Time
	// LastSuccessAt is when the key last served a successful attempt; zero when
	// it never has (within this process's lifetime).
	LastSuccessAt time.Time
}
