package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/yermakovsa/rcpx"
)

func main() {
	const timeout = 20 * time.Second

	upstreams := splitNonEmpty(os.Getenv("RCPX_UPSTREAMS"))
	if len(upstreams) < 2 {
		fmt.Fprintln(os.Stderr, "need at least 2 upstream URLs (comma-separated) in RCPX_UPSTREAMS")
		fmt.Fprintln(os.Stderr, `example: RCPX_UPSTREAMS="https://alchemy...,https://quicknode..." go run .`)
		os.Exit(2)
	}

	rt, err := rcpx.NewRoundTripper(rcpx.Config{
		Upstreams: upstreams,
		Base:      http.DefaultTransport,
		// Cooldown defaults enabled.
		// AllowNonIdempotent defaults false (safe).
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "create rcpx transport: %v\n", err)
		os.Exit(1)
	}

	httpClient := &http.Client{
		Timeout:   timeout,
		Transport: rt,
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// The dial URL can be any upstream; rcpx selects the actual upstream per attempt.
	rpcClient, err := rpc.DialOptions(ctx, upstreams[0], rpc.WithHTTPClient(httpClient))
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial rpc: %v\n", err)
		os.Exit(1)
	}
	defer rpcClient.Close()

	ec := ethclient.NewClient(rpcClient)

	chainID, err := ec.ChainID(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "chain id: %v\n", err)
		os.Exit(1)
	}

	blockNum, err := ec.BlockNumber(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "block number: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("ok chainID=%s blockNumber=%d\n", chainID, blockNum)
}

func splitNonEmpty(csv string) []string {
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}
