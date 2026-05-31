package rcpx

import (
	"context"
	"errors"
)

// RetryPolicy decides whether rcpx should continue to another upstream after a
// non-success attempt. RoundTrip enforces cancellation and idempotency rails
// before calling it.
type RetryPolicy func(out AttemptOutcome) (retry bool)

// AttemptOutcome describes the outcome of one upstream attempt.
//
// It intentionally excludes *http.Response to avoid response-body lifecycle issues.
type AttemptOutcome struct {
	Attempt  int
	Upstream string

	// JSON-RPC request info (best-effort).
	Method string
	Batch  bool

	// StatusCode is 0 when no HTTP response was obtained.
	StatusCode int
	Err        error

	RetryableByDefault bool // whether rcpx classifies this outcome as retryable
}

func defaultRetryPolicy(out AttemptOutcome) bool {
	return out.RetryableByDefault
}

func isBuiltInRetryableStatus(code int) bool {
	switch code {
	case 429, 502, 503, 504:
		return true
	default:
		return false
	}
}

func (cfg resolvedConfig) isRetryableStatus(code int) bool {
	if cfg.retryableStatuses == nil {
		return isBuiltInRetryableStatus(code)
	}

	_, ok := cfg.retryableStatuses[code]
	return ok
}

func (cfg resolvedConfig) isAttemptSuccess(statusCode int, err error) bool {
	return err == nil && !cfg.isRetryableStatus(statusCode)
}

func (cfg resolvedConfig) retryableByOutcome(statusCode int, err error) bool {
	if err != nil {
		return true
	}
	return cfg.isRetryableStatus(statusCode)
}

// statusCode should be 0 when no HTTP response was obtained.
func (cfg resolvedConfig) buildAttemptOutcome(attempt int, upstream, method string, batch bool, statusCode int, err error) AttemptOutcome {
	return AttemptOutcome{
		Attempt:            attempt,
		Upstream:           upstream,
		Method:             method,
		Batch:              batch,
		StatusCode:         statusCode,
		Err:                err,
		RetryableByDefault: cfg.retryableByOutcome(statusCode, err),
	}
}

// shouldContinue decides whether rcpx should try another upstream after a
// non-success attempt.
//
// RoundTrip enforces rails before calling this:
//   - cancellation/deadline returns immediately (policy not called)
//   - non-idempotent safety cannot be overridden unless AllowNonIdempotent is true
func shouldContinue(policy RetryPolicy, out AttemptOutcome) bool {
	if policy == nil {
		return defaultRetryPolicy(out)
	}
	return policy(out)
}

func isCanceledOrDeadline(ctx context.Context, err error) bool {
	if ctx != nil {
		if ctxErr := ctx.Err(); errors.Is(ctxErr, context.Canceled) || errors.Is(ctxErr, context.DeadlineExceeded) {
			return true
		}
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
