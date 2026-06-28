package rcpx

import (
	"errors"
	"testing"
	"time"
)

const upstream = "https://u1.test/rpc"

func newTransportTest(t *testing.T, cfg Config) *transport {
	t.Helper()

	rt, err := NewRoundTripper(cfg)
	if err != nil {
		t.Fatalf("NewRoundTripper() error: %v", err)
	}

	tr, ok := rt.(*transport)
	if !ok {
		t.Fatalf("NewRoundTripper() returned %T, want *transport", rt)
	}

	return tr
}

func TestNewRoundTripper_validation(t *testing.T) {
	t.Run("no upstreams", func(t *testing.T) {
		_, err := NewRoundTripper(Config{})
		if !errors.Is(err, ErrNoUpstreams) {
			t.Fatalf("expected ErrNoUpstreams, got %v", err)
		}
	})

	t.Run("invalid upstream url", func(t *testing.T) {
		tests := []struct {
			name string
			url  string
		}{
			{name: "non-absolute", url: "u1.test/rpc"},
			{name: "bad-scheme", url: "ftp://u1.test/rpc"},
			{name: "missing-host", url: "https:///rpc"},
			{name: "garbage", url: "://"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := NewRoundTripper(Config{Upstreams: []string{tt.url}})
				if err == nil {
					t.Fatalf("expected error for upstream %q, got nil", tt.url)
				}
			})
		}
	})

	t.Run("negative body buffer bytes", func(t *testing.T) {
		_, err := NewRoundTripper(Config{
			Upstreams:       []string{upstream},
			BodyBufferBytes: -1,
		})
		if err == nil {
			t.Fatal("expected error for negative BodyBufferBytes, got nil")
		}
	})

	t.Run("cooldown rejects negatives", func(t *testing.T) {
		t.Run("negative fail after", func(t *testing.T) {
			_, err := NewRoundTripper(Config{
				Upstreams: []string{upstream},
				Cooldown:  &CooldownConfig{FailAfterConsecutive: -1, Duration: time.Second},
			})
			if err == nil {
				t.Fatal("expected error for negative FailAfterConsecutive, got nil")
			}
		})

		t.Run("negative duration", func(t *testing.T) {
			_, err := NewRoundTripper(Config{
				Upstreams: []string{upstream},
				Cooldown:  &CooldownConfig{FailAfterConsecutive: 1, Duration: -1 * time.Second},
			})
			if err == nil {
				t.Fatal("expected error for negative Duration, got nil")
			}
		})
	})

	t.Run("invalid additional retryable status codes", func(t *testing.T) {
		tests := []struct {
			name string
			code int
		}{
			{name: "negative", code: -1},
			{name: "zero", code: 0},
			{name: "below three digits", code: 99},
			{name: "above three digits", code: 1000},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := NewRoundTripper(Config{
					Upstreams:                      []string{upstream},
					AdditionalRetryableStatusCodes: []int{tt.code},
				})
				if err == nil {
					t.Fatalf("expected error for status code %d, got nil", tt.code)
				}
			})
		}
	})

	t.Run("valid additional retryable status code is accepted", func(t *testing.T) {
		_, err := NewRoundTripper(Config{
			Upstreams:                      []string{upstream},
			AdditionalRetryableStatusCodes: []int{500},
		})
		if err != nil {
			t.Fatalf("expected valid status code to be accepted, got %v", err)
		}
	})

	t.Run("duplicate additional retryable status codes are accepted", func(t *testing.T) {
		_, err := NewRoundTripper(Config{
			Upstreams:                      []string{upstream},
			AdditionalRetryableStatusCodes: []int{500, 500},
		})
		if err != nil {
			t.Fatalf("expected duplicate status codes to be accepted, got %v", err)
		}
	})

	t.Run("invalid additional non idempotent methods", func(t *testing.T) {
		tests := []struct {
			name   string
			method string
		}{
			{name: "empty", method: ""},
			{name: "whitespace only", method: " \t\n"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := NewRoundTripper(Config{
					Upstreams:                      []string{upstream},
					AdditionalNonIdempotentMethods: []string{tt.method},
				})
				if err == nil {
					t.Fatalf("expected error for method %q, got nil", tt.method)
				}
			})
		}
	})

	t.Run("duplicate additional non idempotent methods are accepted", func(t *testing.T) {
		_, err := NewRoundTripper(Config{
			Upstreams:                      []string{upstream},
			AdditionalNonIdempotentMethods: []string{"custom_send", "custom_send"},
		})
		if err != nil {
			t.Fatalf("expected duplicate methods to be accepted, got %v", err)
		}
	})
}

func TestNewRoundTripper_defaults(t *testing.T) {
	t.Run("cooldown nil uses defaults", func(t *testing.T) {
		tr := newTransportTest(t, Config{Upstreams: []string{upstream}})

		if !tr.cfg.cooldown.enabled {
			t.Fatal("expected cooldown enabled by default")
		}
		if tr.cfg.cooldown.failAfter != DefaultCooldownFailAfterConsecutive {
			t.Fatalf("expected failAfter=%d, got %d", DefaultCooldownFailAfterConsecutive, tr.cfg.cooldown.failAfter)
		}
		if tr.cfg.cooldown.duration != DefaultCooldownDuration {
			t.Fatalf("expected duration=%s, got %s", DefaultCooldownDuration, tr.cfg.cooldown.duration)
		}
	})

	t.Run("cooldown disabled", func(t *testing.T) {
		tr := newTransportTest(t, Config{
			Upstreams: []string{upstream},
			Cooldown:  &CooldownConfig{Disabled: true},
		})

		if tr.cfg.cooldown.enabled {
			t.Fatal("expected cooldown disabled")
		}
		// When disabled, other values are inert.
	})

	t.Run("cooldown zero values use defaults", func(t *testing.T) {
		tr := newTransportTest(t, Config{
			Upstreams: []string{upstream},
			Cooldown:  &CooldownConfig{FailAfterConsecutive: 0, Duration: 0},
		})

		if !tr.cfg.cooldown.enabled {
			t.Fatal("expected cooldown enabled")
		}
		if tr.cfg.cooldown.failAfter != DefaultCooldownFailAfterConsecutive {
			t.Fatalf("expected failAfter=%d, got %d", DefaultCooldownFailAfterConsecutive, tr.cfg.cooldown.failAfter)
		}
		if tr.cfg.cooldown.duration != DefaultCooldownDuration {
			t.Fatalf("expected duration=%s, got %s", DefaultCooldownDuration, tr.cfg.cooldown.duration)
		}
	})

	t.Run("body buffer bytes defaults", func(t *testing.T) {
		tr := newTransportTest(t, Config{Upstreams: []string{upstream}})

		if tr.cfg.bodyCap != DefaultBodyBufferBytes {
			t.Fatalf("expected bodyCap=%d, got %d", DefaultBodyBufferBytes, tr.cfg.bodyCap)
		}
	})

	t.Run("cooldown deadline exceeded counting defaults disabled", func(t *testing.T) {
		tr := newTransportTest(t, Config{Upstreams: []string{upstream}})

		if tr.cfg.cooldown.countDeadlineExceeded {
			t.Fatal("expected CountDeadlineExceeded to default false")
		}
	})

	t.Run("cooldown deadline exceeded counting can be enabled", func(t *testing.T) {
		tr := newTransportTest(t, Config{
			Upstreams: []string{upstream},
			Cooldown: &CooldownConfig{
				CountDeadlineExceeded: true,
			},
		})

		if !tr.cfg.cooldown.countDeadlineExceeded {
			t.Fatal("expected CountDeadlineExceeded to resolve true")
		}
	})
}
