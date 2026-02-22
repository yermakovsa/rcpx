# rcpx

rcpx is a Go library that provides an HTTP JSON-RPC failover `http.RoundTripper`.

Configure an `http.Client` to use rcpx as its transport. For each request, rcpx tries upstream URLs in priority order until one succeeds, based on a retry policy and safety rails.

## Key behavior

* Tries upstreams sequentially, in priority order.
* Tries each eligible upstream at most once per request.
* By default, continues to the next upstream on any transport error, or on HTTP status `429`, `502`, `503`, `504`.
* Treats HTTP statuses other than 429/502/503/504 as success and returns them unchanged (even if non-2xx)
* If the request context is canceled or deadline exceeded, returns immediately and does not consult the retry policy.
* Buffers the request body once per request so it can resend it across upstreams (capped by `BodyBufferBytes`).
* Cooldown is enabled by default and can temporarily skip upstreams after consecutive failures.
* By default, does not retry or fail over non-idempotent JSON-RPC methods (see `AllowNonIdempotent`).

## API at a glance

* `rcpx.NewRoundTripper(cfg rcpx.Config) (http.RoundTripper, error)`

## When to use rcpx

Use rcpx if you have multiple HTTP JSON-RPC endpoints and want in-process sequential failover (for example, primary + backup RPC providers).

Do not use rcpx if you need per-upstream auth headers, WebSocket subscriptions, quorum/hedged requests, or gateway/proxy features. rcpx is an HTTP `RoundTripper` and routes each request to one upstream at a time.

## Constraints and non-goals

* Provider auth is expected to be encoded in the upstream URL (path/query).
* Per-upstream header customization is not supported.
* Upstreams are full target URLs; requests are sent to that exact URL (no path joining).
* The returned transport is safe for concurrent use; concurrency characteristics also depend on the provided base transport.

## Installation

```bash
go get github.com/yermakovsa/rcpx
```

Requires Go 1.24 (per `go.mod`).

## Quick start (go-ethereum)

rcpx is designed to be used as the HTTP transport behind go-ethereum `rpc` and `ethclient`. You keep using those clients normally; rcpx selects the upstream per attempt.

rcpx rewrites `req.URL` on each attempt. You can use any configured upstream as the initial dial URL; using `Upstreams[0]` keeps the example straightforward.

```go
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/yermakovsa/rcpx"
)

func main() {
	const timeout = 20 * time.Second

	upstreams := []string{
		"https://rpc.example.com/?key=YOUR_KEY",
		"https://backup.example.com/?key=YOUR_KEY",
	}

	transport, err := rcpx.NewRoundTripper(rcpx.Config{
		Upstreams: upstreams,
	})
	if err != nil {
		log.Fatalf("create rcpx transport: %v", err)
	}

	httpClient := &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}

	ctxDial, cancelDial := context.WithTimeout(context.Background(), timeout)
	defer cancelDial()

	// The dial URL can be any upstream; rcpx rewrites req.URL per attempt.
	rpcClient, err := rpc.DialOptions(ctxDial, upstreams[0], rpc.WithHTTPClient(httpClient))
	if err != nil {
		log.Fatalf("dial rpc: %v", err)
	}
	defer rpcClient.Close()

	ec := ethclient.NewClient(rpcClient)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	chainID, err := ec.ChainID(ctx)
	if err != nil {
		log.Fatalf("chain id: %v", err)
	}
	blockNum, err := ec.BlockNumber(ctx)
	if err != nil {
		log.Fatalf("block number: %v", err)
	}

	fmt.Printf("ok chainID=%s blockNumber=%d\n", chainID, blockNum)
}
```

## Quick start (net/http)

Create a transport and plug it into an `http.Client`.

rcpx rewrites `req.URL` on each attempt. You can use any configured upstream as the initial request URL; using `Upstreams[0]` keeps the example straightforward.

```go
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/yermakovsa/rcpx"
)

func main() {
	const timeout = 10 * time.Second

	upstreams := []string{
		"https://rpc.example.com/?key=YOUR_KEY",
		"https://backup.example.com/?key=YOUR_KEY",
	}

	transport, err := rcpx.NewRoundTripper(rcpx.Config{
		Upstreams: upstreams,
	})
	if err != nil {
		log.Fatalf("create rcpx transport: %v", err)
	}

	httpClient := &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// The request URL can be any valid URL; rcpx rewrites req.URL per attempt.
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		upstreams[0],
		bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`)),
	)
	if err != nil {
		log.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		log.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		log.Fatalf("read response: %v", err)
	}

	fmt.Println("status:", resp.Status)
	fmt.Println("body (first 1KiB):", string(body))
}
```

## Configuration overview

rcpx is configured via `rcpx.Config`.

### How failover works

* Upstreams are tried sequentially, in priority order.
* Each eligible upstream is tried at most once per request.
* By default, rcpx continues to the next upstream on any transport error, or on HTTP status `429`, `502`, `503`, `504`.
* An attempt succeeds when `err == nil` and the status code is not `429`, `502`, `503`, or `504`.
* Other HTTP status codes are treated as success from rcpx's perspective and returned unchanged.
* If the request context is canceled or deadline exceeded, rcpx returns immediately and does not consult the retry policy.

### Upstreams

```go
type Config struct {
	Upstreams []string
	// ...
}
```

* `Upstreams` are tried in priority order.
* Each entry must be an absolute `http` or `https` URL.
* Requests are sent to that exact URL (scheme/host/path/query); there is no path joining.

If `Upstreams` is empty, `rcpx.NewRoundTripper` returns `rcpx.ErrNoUpstreams`.

### Base transport

```go
type Config struct {
	Base http.RoundTripper
	// ...
}
```

* `Base` is the underlying transport used for all attempts.
* If `Base` is nil, rcpx uses `http.DefaultTransport`.
* TLS, proxies, timeouts, and connection pooling come from the base transport and the `http.Client` you use.

rcpx handles misbehaving base transports defensively:

* It will not return `(nil, nil)` from `RoundTrip`; if that happens, rcpx returns an error.
* If a base transport returns both `resp != nil` and `err != nil`, rcpx closes `resp.Body` and treats it as an error to avoid leaks.

### Retry policy

```go
type RetryPolicy func(out rcpx.AttemptOutcome) (retry bool)
```

You can override the default retry or failover behavior with `Config.RetryPolicy`. The policy is only consulted after a non-success attempt when there is another eligible upstream to try.

`AttemptOutcome` includes:

* `Attempt` (1-based attempt number for the current request)
* `Upstream` (the upstream URL attempted)
* JSON-RPC info (best-effort): `Method`, `Batch`
* `StatusCode` (0 if no HTTP response was obtained)
* `Err` (the error from the base transport, if any)
* `RetryableByDefault` (rcpx's default classification for this outcome)

`RetryableByDefault` is true for transport errors (`Err != nil`) and for HTTP status `429`, `502`, `503`, `504`.

Because the policy is only consulted on non-success attempts, it cannot make additional HTTP status codes (for example, 500) retryable.

### Request body buffering cap

```go
type Config struct {
	BodyBufferBytes int
	// ...
}
```

rcpx buffers the request body once per request so it can resend it across upstreams.

* `BodyBufferBytes == 0` uses `rcpx.DefaultBodyBufferBytes` (1 MiB).
* `BodyBufferBytes < 0` is invalid and causes `NewRoundTripper` to return an error.
* If the request body exceeds the cap, the request fails with `rcpx.ErrBodyTooLarge`.
* If the request body cannot be read, the request fails with an error that joins `rcpx.ErrBodyUnreadable` with the underlying read error.

### Cooldown

```go
type Config struct {
	Cooldown *rcpx.CooldownConfig
	// ...
}

type CooldownConfig struct {
	Disabled             bool
	FailAfterConsecutive int
	Duration             time.Duration
}
```

Cooldown is enabled by default (when `Cooldown` is nil).

Behavior:

* Cooldown is tracked per-upstream.
* Only failures that caused rcpx to continue to another upstream count toward the consecutive failure threshold.
* Once an upstream hits the threshold, it is skipped for the configured duration.
* A successful attempt on an upstream resets its cooldown counters.

Configuration:

* Set `Cooldown: &rcpx.CooldownConfig{Disabled: true}` to turn cooldown off.
* When enabled:

  * `FailAfterConsecutive == 0` uses `rcpx.DefaultCooldownFailAfterConsecutive` (3).
  * `Duration == 0` uses `rcpx.DefaultCooldownDuration` (30s).
* Negative values for `FailAfterConsecutive` or `Duration` are invalid and cause `NewRoundTripper` to return an error.

When all upstreams are cooling down, requests fail with `*rcpx.AllUpstreamsFailedError` where `Attempted == 0`. (Its `Unwrap()` reports `rcpx.ErrNoEligibleUpstreams`.)

### Non-idempotent safety rail

```go
type Config struct {
	AllowNonIdempotent bool
	// ...
}
```

By default, rcpx will not retry or fail over non-idempotent JSON-RPC methods.

* If `AllowNonIdempotent` is false (default) and a request is classified as non-idempotent, rcpx may attempt it once, but it will not continue to another upstream even if the retry policy would otherwise continue.
* In that case, rcpx returns `*rcpx.NonIdempotentBlockedError` wrapping the underlying failure cause.

Current non-idempotent method list (built-in):

* `eth_sendTransaction`
* `eth_sendRawTransaction`

Batch requests are treated conservatively:

* If any item in the batch is unknown or unparseable for method extraction, the whole batch is treated as non-idempotent.
* If method extraction fails (`ok == false`), rcpx treats the request as non-idempotent.

If you set `AllowNonIdempotent` to true, rcpx can fail over even for these methods. This can duplicate side effects. Use with care.

## Performance and keep-alives

* Connection reuse and pooling behavior comes from the base transport (`Config.Base`). If you use `http.DefaultTransport` (a `*http.Transport`), keep-alive pools are per host.
* If the primary upstream is healthy, rcpx adds per-request overhead from buffering the request body (up to `BodyBufferBytes`) and best-effort JSON-RPC method parsing.
* The first failover to a cold secondary may pay a handshake once; after that it can reuse connections like any other HTTP client.
* Tip: if you use a custom `*http.Transport` as `Base`, tune it for your workload (for example, `MaxIdleConnsPerHost`, `MaxConnsPerHost`).

## Error handling

rcpx uses sentinel errors for common cases, plus typed errors that carry per-attempt detail. The typed errors support `errors.Is` and `errors.As` via `Unwrap()`.

For runnable error inspection examples, see `examples/goethereum/error-inspection`.

### Sentinel errors

* `rcpx.ErrNoUpstreams`
  Returned when `Config.Upstreams` is empty.

* `rcpx.ErrNoEligibleUpstreams`
  Indicates that no upstreams were eligible to try (for example, all cooling down). This is returned via `AllUpstreamsFailedError.Unwrap()` when `Attempted == 0`.

* `rcpx.ErrBodyTooLarge`
  Returned when the request body exceeds the configured buffer cap.

* `rcpx.ErrBodyUnreadable`
  Returned (joined with an underlying error) when the request body cannot be read.

### `AllUpstreamsFailedError`

`*rcpx.AllUpstreamsFailedError` is returned when no upstream attempt succeeded.

Fields:

* `Attempted`
  Number of attempts made. If `Attempted == 0`, no upstreams were eligible.
* `SkippedCooldown`
  How many upstreams were skipped due to cooldown.
* `Failures []rcpx.AttemptFailure`
  Failures in attempt order (one per attempt that rcpx recorded).

`Unwrap()` behavior:

* If `Attempted == 0`, `Unwrap()` returns `rcpx.ErrNoEligibleUpstreams`.
* Otherwise, `Unwrap()` returns the last non-nil underlying failure error recorded in `Failures`.

Each `AttemptFailure` includes:

* `Upstream` (string)
* JSON-RPC info (best-effort): `Method`, `Batch`
* `StatusCode` (0 if no HTTP response was obtained)
* `Err` (the failure cause)
* `Retryable` (whether rcpx continued after this attempt)

Snippet:

```go
var ae *rcpx.AllUpstreamsFailedError
if errors.As(err, &ae) {
	fmt.Printf("attempted=%d skippedCooldown=%d failures=%d\n",
		ae.Attempted, ae.SkippedCooldown, len(ae.Failures))

	for i, f := range ae.Failures {
		fmt.Printf("  #%d upstream=%s status=%d retryable=%v err=%v\n",
			i+1, f.Upstream, f.StatusCode, f.Retryable, f.Err)
	}

	fmt.Printf("errors.Is(ErrNoEligibleUpstreams)=%v\n",
		errors.Is(err, rcpx.ErrNoEligibleUpstreams),
	)
}
```

### `NonIdempotentBlockedError`

`*rcpx.NonIdempotentBlockedError` is returned when a request classified as non-idempotent would otherwise retry or fail over, but `AllowNonIdempotent` is false.

It includes:

* `Outcome rcpx.AttemptOutcome` (the attempt outcome that would have been used for policy decisions)
* `Cause error` (the underlying failure cause)

`Unwrap()` returns `Cause`.

Snippet:

```go
var be *rcpx.NonIdempotentBlockedError
if errors.As(err, &be) {
	fmt.Printf("blocked method=%s retryableByDefault=%v\n",
		be.Outcome.Method, be.Outcome.RetryableByDefault,
	)
	fmt.Printf("cause=%v\n", be.Unwrap())
}
```

### Cancellation and deadlines

If the request context is canceled or its deadline is exceeded, rcpx returns immediately. You can check for these conditions with `errors.Is`:

```go
if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
	// request was canceled or timed out
}
```

## Examples reference

All examples are runnable under `examples/goethereum/` and use rcpx as the HTTP transport behind go-ethereum `rpc` / `ethclient`.

### `examples/goethereum/basic-failover`

* Shows: Basic wiring: build an rcpx `http.RoundTripper`, plug it into an `http.Client`, and pass that client to `rpc.DialOptions` via `rpc.WithHTTPClient`.
* Calls: `ChainID` and `BlockNumber`.
* Env: Uses `RCPX_UPSTREAMS` (comma-separated) and requires at least 2 URLs.

### `examples/goethereum/cooldown`

* Shows: Cooldown behavior.
* Setup: Wraps the base transport to log each attempted URL and count hits per upstream; configures `CooldownConfig` with `FailAfterConsecutive=1` and `Duration=30s`.
* Env: Uses `RCPX_UPSTREAMS`.

### `examples/goethereum/non-idempotent-default-block`

* Shows: The default non-idempotent safety rail.
* Setup: Uses intentionally failing upstreams (closed local ports) to trigger a retryable failure, then calls `eth_sendRawTransaction`.
* Expected: `*rcpx.NonIdempotentBlockedError` (checked via `errors.As`).

### `examples/goethereum/non-idempotent-allow`

* Shows: Opting in to failover for non-idempotent methods with `AllowNonIdempotent: true`.
* Setup: Uses intentionally failing upstreams (closed local ports) and calls `eth_sendRawTransaction`.
* Expected: `*rcpx.AllUpstreamsFailedError`, then prints attempt details from `Failures`.

### `examples/goethereum/error-inspection`

* Shows: `errors.As` and `errors.Is` for `*rcpx.AllUpstreamsFailedError` and `*rcpx.NonIdempotentBlockedError`.

## License

MIT. See `LICENSE`.
