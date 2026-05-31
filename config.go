package rcpx

import (
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Config configures an rcpx RoundTripper.
//
// It selects which upstream URL to use per attempt. The caller controls
// TLS/proxy/timeouts via the base transport.
type Config struct {
	// Upstreams are tried in priority order. Each entry must be an absolute
	// http/https URL. Requests are sent to that exact URL (no path joining).
	Upstreams []string

	// Base transport used for all attempts. If nil, http.DefaultTransport is used.
	Base http.RoundTripper

	// Cooldown behavior after consecutive retryable failures. If nil, cooldown is
	// enabled with defaults.
	Cooldown *CooldownConfig

	// If false, rcpx will not retry/failover non-idempotent methods.
	AllowNonIdempotent bool

	// Per-request request-body buffer cap in bytes.
	// 0 => DefaultBodyBufferBytes
	// <0 => invalid
	BodyBufferBytes int

	// Retry/failover policy hook. If nil, the default policy is used.
	RetryPolicy RetryPolicy

	// AdditionalRetryableStatusCodes are HTTP status codes retried in addition to
	// the defaults: 429, 502, 503, and 504.
	AdditionalRetryableStatusCodes []int
}

// CooldownConfig configures cooldown behavior.
//
// Zero values use defaults; set Disabled to turn cooldown off.
type CooldownConfig struct {
	Disabled bool

	// 0 => DefaultCooldownFailAfterConsecutive (unless Disabled=true)
	FailAfterConsecutive int

	// 0 => DefaultCooldownDuration (unless Disabled=true)
	Duration time.Duration
}

// resolvedUpstream is the normalized form of a user-provided upstream string.
type resolvedUpstream struct {
	raw string
	url *url.URL
}

type effectiveCooldown struct {
	enabled   bool
	failAfter int
	duration  time.Duration
}

// resolvedConfig is the internal, fully-normalized configuration used at runtime.
type resolvedConfig struct {
	upstreams []resolvedUpstream
	base      http.RoundTripper
	cooldown  effectiveCooldown
	allowNI   bool
	bodyCap   int
	policy    RetryPolicy

	retryableStatuses map[int]struct{}
}

func resolveConfig(cfg Config) (resolvedConfig, error) {
	if len(cfg.Upstreams) == 0 {
		return resolvedConfig{}, ErrNoUpstreams
	}

	upstreams := make([]resolvedUpstream, 0, len(cfg.Upstreams))
	for _, raw := range cfg.Upstreams {
		u, err := url.Parse(raw)
		if err != nil {
			return resolvedConfig{}, fmt.Errorf("rcpx: invalid upstream %q: %w", raw, err)
		}

		// Must be an absolute http/https URL.
		if !u.IsAbs() {
			return resolvedConfig{}, fmt.Errorf("rcpx: invalid upstream %q: must be absolute url", raw)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return resolvedConfig{}, fmt.Errorf("rcpx: invalid upstream %q: unsupported scheme %q", raw, u.Scheme)
		}
		if u.Host == "" {
			return resolvedConfig{}, fmt.Errorf("rcpx: invalid upstream %q: missing host", raw)
		}

		// Preserve as parsed; contract is “send to that URL” (scheme/host/path/query).
		upstreams = append(upstreams, resolvedUpstream{raw: raw, url: u})
	}

	base := cfg.Base
	if base == nil {
		base = http.DefaultTransport
	}

	bodyCap := cfg.BodyBufferBytes
	if bodyCap < 0 {
		return resolvedConfig{}, fmt.Errorf("rcpx: invalid BodyBufferBytes %d", bodyCap)
	}
	if bodyCap == 0 {
		bodyCap = DefaultBodyBufferBytes
	}

	cooldown, err := resolveCooldown(cfg.Cooldown)
	if err != nil {
		return resolvedConfig{}, err
	}

	policy := cfg.RetryPolicy
	if policy == nil {
		policy = defaultRetryPolicy
	}

	statuses, err := resolveRetryableStatusCodes(cfg.AdditionalRetryableStatusCodes)
	if err != nil {
		return resolvedConfig{}, err
	}

	return resolvedConfig{
		upstreams: upstreams,
		base:      base,
		cooldown:  cooldown,
		allowNI:   cfg.AllowNonIdempotent,
		bodyCap:   bodyCap,
		policy:    policy,

		retryableStatuses: statuses,
	}, nil
}

func resolveCooldown(cc *CooldownConfig) (effectiveCooldown, error) {
	if cc == nil {
		return effectiveCooldown{
			enabled:   true,
			failAfter: DefaultCooldownFailAfterConsecutive,
			duration:  DefaultCooldownDuration,
		}, nil
	}

	if cc.Disabled {
		return effectiveCooldown{enabled: false}, nil
	}

	if cc.FailAfterConsecutive < 0 {
		return effectiveCooldown{}, fmt.Errorf("rcpx: invalid Cooldown.FailAfterConsecutive %d", cc.FailAfterConsecutive)
	}
	if cc.Duration < 0 {
		return effectiveCooldown{}, fmt.Errorf("rcpx: invalid Cooldown.Duration %s", cc.Duration)
	}

	failAfter := cc.FailAfterConsecutive
	if failAfter == 0 {
		failAfter = DefaultCooldownFailAfterConsecutive
	}

	dur := cc.Duration
	if dur == 0 {
		dur = DefaultCooldownDuration
	}

	return effectiveCooldown{
		enabled:   true,
		failAfter: failAfter,
		duration:  dur,
	}, nil
}

func resolveRetryableStatusCodes(additional []int) (map[int]struct{}, error) {
	statuses := map[int]struct{}{
		429: {},
		502: {},
		503: {},
		504: {},
	}

	for i, code := range additional {
		if !validHTTPStatusCode(code) {
			return nil, fmt.Errorf("rcpx: invalid AdditionalRetryableStatusCodes[%d] %d", i, code)
		}
		statuses[code] = struct{}{}
	}

	return statuses, nil
}

func validHTTPStatusCode(code int) bool {
	return code >= 100 && code <= 999
}
