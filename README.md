# rcpx

rcpx is a small Go library that provides an HTTP JSON-RPC failover `http.RoundTripper`, mainly for Go applications using clients such as `go-ethereum/rpc` and `ethclient`.

Configure an `http.Client` to use rcpx as its transport. For each request, rcpx tries upstream URLs in priority order until one succeeds, based on a retry policy and safety rails.

## Key behavior

* Tries upstreams sequentially, in priority order.
* Tries each eligible upstream at most once per request.
* By default, continues to the next upstream on any transport error, or on HTTP status `429`, `502`, `503`, `504`.
* By default, treats HTTP statuses other than `429`, `502`, `503`, `504` as success and returns them unchanged, even if non-2xx.
* Does not inspect JSON-RPC response bodies; HTTP `200` with a JSON-RPC error body is returned unchanged.
* If the request context is canceled or deadline exceeded, returns immediately and does not consult the retry policy.
* Buffers the request body once per request so it can resend it across upstreams, capped by `BodyBufferBytes`.
* Cooldown is enabled by default and can temporarily skip upstreams after consecutive failover-causing failures.
* By default, does not retry or fail over non-idempotent JSON-RPC methods such as `eth_sendRawTransaction` and `eth_sendTransaction`.

## API at a glance

* `rcpx.NewRoundTripper(cfg rcpx.Config) (http.RoundTripper, error)`

## When to use rcpx

Use rcpx if you have multiple HTTP JSON-RPC endpoints and want in-process sequential failover, for example a primary RPC provider plus one or more backup providers.

rcpx is most useful when upstreams have a clear priority order and you want explicit failover behavior rather than load balancing.

Do not use rcpx if you need per-upstream auth headers, WebSocket subscriptions, quorum/hedged requests, or gateway/proxy features. rcpx is an HTTP `RoundTripper` and routes each request to one upstream at a time.

## What rcpx does not guarantee

rcpx operates at the HTTP request level. It does not validate Ethereum state, compare providers, or coordinate multiple RPC calls that are part of one larger application operation.

In particular, rcpx does not guarantee that:

* all providers are at the same block height;
* providers have the same pending state or mempool view;
* nonce queries are consistent across providers;
* transaction lookups are visible across providers immediately;
* a sequence of separate RPC calls reads from the same provider;
* HTTP `200` responses contain fresh, correct, or globally consistent JSON-RPC data.

For state-sensitive workflows, pin the logical operation to one provider where possible, and use explicit block numbers or block hashes when the RPC method supports them.

## Constraints and non-goals

* Provider auth is expected to be encoded in the upstream URL (path/query).
* Per-upstream header customization is not supported.
* Upstreams are full target URLs; requests are sent to that exact URL (no path joining).
* The returned transport is safe for concurrent use; concurrency characteristics also depend on the provided base transport.
* rcpx does not inspect JSON-RPC response bodies or provide Ethereum provider consistency guarantees.
* rcpx is not a proxy, gateway, hosted service, WebSocket layer, load balancer, quorum requester, hedged requester, transaction manager, nonce manager, or provider consistency layer.

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

Default behavior summary:

| Setting | Default |
|---|---|
| Retryable HTTP statuses | `429`, `502`, `503`, `504` |
| Additional retryable HTTP statuses | None |
| Body buffer cap | `rcpx.DefaultBodyBufferBytes` (`1 MiB`) |
| Cooldown | Enabled |
| Cooldown threshold | `rcpx.DefaultCooldownFailAfterConsecutive` (`3`) consecutive failover-causing failures |
| Cooldown duration | `rcpx.DefaultCooldownDuration` (`30s`) |
| Non-idempotent failover | Disabled |
| Additional non-idempotent methods | None |
| Base transport | `http.DefaultTransport` |

### How failover works

* Upstreams are tried sequentially, in priority order.
* Each eligible upstream is tried at most once per request.
* By default, rcpx continues to the next upstream on any transport error, or on HTTP status `429`, `502`, `503`, `504`.
* An attempt succeeds when `err == nil` and the status code is not retryable.
* Other HTTP status codes are treated as success from rcpx's perspective and returned unchanged.
* JSON-RPC response bodies are not inspected. A JSON-RPC error returned with HTTP `200` is returned unchanged.
* If the request context is canceled or deadline exceeded, rcpx returns immediately and does not consult the retry policy.

Default failover decision summary:

| Outcome | Default rcpx behavior |
|---|---|
| Transport error | Try next eligible upstream |
| HTTP `429`, `502`, `503`, `504` | Try next eligible upstream |
| HTTP `500` | Return unchanged |
| Other non-retryable HTTP status | Return unchanged |
| HTTP `200` with JSON-RPC error body | Return unchanged |
| Context canceled or deadline exceeded | Return immediately |
| Retryable failure for `eth_sendRawTransaction` or `eth_sendTransaction` | Block failover by default |
| All upstreams cooling down | Return `*rcpx.AllUpstreamsFailedError` with `Attempted == 0` |

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

### Additional retryable HTTP statuses

```go
type Config struct {
	AdditionalRetryableStatusCodes []int
	// ...
}
```

`AdditionalRetryableStatusCodes` adds status codes to the built-in retryable set: `429`, `502`, `503`, and `504`.

For example, `[]int{500}` makes HTTP `500` retryable in addition to the defaults. Duplicates are ignored, and values must be three-digit HTTP status codes.

Retryable status classification happens before `RetryPolicy` is called.

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
* `RetryableByDefault` (whether rcpx classifies the outcome as retryable)

`RetryableByDefault` is true for transport errors (`Err != nil`), for HTTP status `429`, `502`, `503`, `504`, and for statuses configured with `AdditionalRetryableStatusCodes`.

The retry policy is only consulted after rcpx has classified an attempt as non-success and there is another eligible upstream to try.

The retry policy is not called:

* on successful attempts;
* for the last eligible upstream;
* when the request context is canceled or its deadline is exceeded.

`RetryPolicy` cannot make a non-retryable HTTP status retryable by itself, because the policy is only called after rcpx has already classified an attempt as non-success. To make an additional HTTP status such as `500` retryable, configure `AdditionalRetryableStatusCodes`.

`RetryPolicy` receives `AttemptOutcome`; it cannot inspect response bodies or response headers. It can decide whether rcpx should continue after an already-classified non-success attempt, but it is not a general response validation hook.

### Attempt observation

```go
type Config struct {
	OnAttempt func(rcpx.AttemptInfo)
	// ...
}

type AttemptInfo struct {
	Attempt    int
	Upstream   string
	Method     string
	Batch      bool
	StatusCode int
	Err        error
	Final      bool
}
```

`OnAttempt`, if set, is called once for each attempted upstream. It can be used for logging or metrics, including recording which upstream handled a successful request.

`AttemptInfo` includes:

* `Attempt` (1-based attempt number for the current request)
* `Upstream` (the configured upstream URL attempted)
* JSON-RPC info (best-effort): `Method`, `Batch`
* `StatusCode` (0 if no HTTP response was obtained)
* `Err` (the attempt failure cause, if any)
* `Final` (whether rcpx will make no further upstream attempts for this request after this attempt)

`Final` is true for the attempt that returns a successful response, and also for terminal failures such as the last eligible upstream failing, retry policy stopping failover, or the non-idempotent safety rail blocking failover.

The callback is called synchronously. If it blocks, the request blocks.

`Upstream` is the configured upstream string and is not redacted. It may contain credentials if your upstream URLs contain credentials.

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

rcpx reads and closes the original request body while buffering it. For each upstream attempt, rcpx sends a cloned request with a fresh reader over the buffered body.

If an attempt receives a response but rcpx decides to fail over, rcpx closes that failed attempt's response body before trying the next upstream.

If rcpx returns a response to the caller, the caller owns that response and must close `resp.Body` as usual.

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

Cooldown is time-based. When the cooldown duration expires, the upstream becomes eligible again; rcpx does not perform a health check before reusing it. Cooldown does not prove that an upstream is fresh, synced, at a particular block height, or returning correct JSON-RPC data.

When all upstreams are cooling down, requests fail with `*rcpx.AllUpstreamsFailedError` where `Attempted == 0`. (Its `Unwrap()` reports `rcpx.ErrNoEligibleUpstreams`.)

### Non-idempotent safety rail

```go
type Config struct {
	AllowNonIdempotent             bool
	AdditionalNonIdempotentMethods []string
	// ...
}
```

By default, rcpx will not retry or fail over non-idempotent JSON-RPC methods.

* If `AllowNonIdempotent` is false (default) and a request is classified as non-idempotent, rcpx may attempt it once, but it will not continue to another upstream even if the retry policy would otherwise continue.
* In that case, rcpx returns `*rcpx.NonIdempotentBlockedError` wrapping the underlying failure cause.

Current non-idempotent method list (built-in):

* `eth_sendTransaction`
* `eth_sendRawTransaction`

`AdditionalNonIdempotentMethods` adds exact JSON-RPC method names to the built-in non-idempotent set. The built-in methods cannot be removed. Duplicate names are ignored, and empty names are invalid.

`AllowNonIdempotent` applies to both built-in and configured methods.

Batch requests are treated conservatively:

* If any item in the batch is unknown or unparseable for method extraction, the whole batch is treated as non-idempotent.
* If method extraction fails (`ok == false`), rcpx treats the request as non-idempotent.

If you set `AllowNonIdempotent` to true, rcpx can fail over even for these methods. This can duplicate side effects. Use with care.

## Ethereum provider consistency caveats

rcpx fails over individual HTTP requests. It does not know when several separate RPC calls are part of one larger application operation.

If failover happens between separate calls, the application may observe data from different providers, different block heights, different chain heads, different pending states, or different mempool views.

A single JSON-RPC batch HTTP request is not split across providers. The whole HTTP request is sent to one upstream per attempt. If that attempt fails in a retryable way, the whole batch may be retried on another upstream.

Be careful with workflows involving:

* multiple related `eth_call` requests;
* `eth_getTransactionCount` with the `pending` tag;
* transaction submission followed by transaction lookup;
* nonce management;
* pending transaction tracking;
* read-after-write assumptions.

For state-sensitive workflows, prefer pinning the whole logical operation to one provider where possible, and use explicit block numbers or block hashes when the RPC method supports them.

rcpx can improve availability when an upstream fails at the HTTP transport/status level. It does not guarantee Ethereum state consistency across providers.

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
* Calls: Read-only methods: `ChainID` and `BlockNumber`.
* Env: Uses `RCPX_UPSTREAMS` (comma-separated) and requires at least 2 URLs.
* Expected behavior: If the first upstream fails in a retryable way, rcpx tries the next eligible upstream.

### `examples/goethereum/cooldown`

* Shows: Cooldown behavior.
* Setup: Wraps the base transport to log each attempted URL and count hits per upstream; configures `CooldownConfig` with `FailAfterConsecutive=1` and `Duration=30s`.
* Env: Uses `RCPX_UPSTREAMS`.
* Expected behavior: For the clearest demo, set the first upstream to an unreachable URL and the second to a working RPC URL. After the first upstream fails and rcpx fails over, later requests should skip the first upstream while it is cooling down.

### `examples/goethereum/non-idempotent-default-block`

* Shows: The default non-idempotent safety rail.
* Setup: Uses intentionally failing upstreams (closed local ports) to trigger a retryable failure, then calls `eth_sendRawTransaction`.
* Expected: `*rcpx.NonIdempotentBlockedError` (checked via `errors.As`).
* Note: The closed local ports are intentional so the example does not submit a transaction to a real provider.

### `examples/goethereum/non-idempotent-allow`

* Shows: Opting in to failover for non-idempotent methods with `AllowNonIdempotent: true`.
* Setup: Uses intentionally failing upstreams (closed local ports) and calls `eth_sendRawTransaction`.
* Expected: `*rcpx.AllUpstreamsFailedError`, then prints attempt details from `Failures`.

### `examples/goethereum/error-inspection`

* Shows: `errors.As` and `errors.Is` for `*rcpx.AllUpstreamsFailedError` and `*rcpx.NonIdempotentBlockedError`.
* Expected behavior: Demonstrates how to distinguish exhausted upstream attempts from failover blocked by the non-idempotent safety rail.

## License

MIT. See `LICENSE`.
