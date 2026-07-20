package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/baleen37/agent-remote-sessions/internal/protocol"
	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/runtime"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ars-collector: remote home unavailable")
		os.Exit(1)
	}
	os.Exit(run(context.Background(), os.Args[1:], home, provider.Builtin(), os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, home string, adapters []provider.Adapter, stdout, stderr io.Writer) int {
	return runWithRuntime(ctx, args, home, adapters, runtime.SystemRunner{}, stdout, stderr)
}

func runWithRuntime(ctx context.Context, args []string, home string, adapters []provider.Adapter, runtimeRunner runtime.Runner, stdout, stderr io.Writer) int {
	if len(args) != 1 || !validNonce(args[0]) {
		fmt.Fprintln(stderr, "ars-collector: expected one 128-bit hexadecimal nonce")
		return 2
	}
	candidates, results, err := provider.DiscoverAll(ctx, home, adapters)
	if err != nil {
		fmt.Fprintln(stderr, "ars-collector: provider discovery failed")
		return 1
	}
	for _, result := range results {
		if diagnostic := providerDiagnostic(result); diagnostic != "" {
			fmt.Fprintln(stderr, diagnostic)
		}
	}

	states, report := runtime.Inspect(ctx, runtimeRunner, candidates)
	discovered := combineRuntime(candidates, states)
	if err := protocol.Encode(stdout, args[0], discovered, results, report); err != nil {
		fmt.Fprintln(stderr, "ars-collector: encode failed")
		return 1
	}
	return 0
}

func combineRuntime(candidates []session.Candidate, states map[string]session.Runtime) []session.Discovered {
	discovered := make([]session.Discovered, len(candidates))
	for i, candidate := range candidates {
		state, ok := states[runtime.Key(string(candidate.Provider), candidate.NativeID)]
		if !ok {
			state = session.Runtime{State: session.RuntimeSaved}
		}
		discovered[i] = session.Discovered{Candidate: candidate, Runtime: state}
	}
	return discovered
}

func validNonce(nonce string) bool {
	if len(nonce) != 32 {
		return false
	}
	_, err := hex.DecodeString(nonce)
	return err == nil
}

func providerDiagnostic(result provider.Result) string {
	if result.Status != provider.Partial && result.Status != provider.Error {
		return ""
	}
	switch result.ErrorCode {
	case "unavailable", "incompatible", "corrupt", "resource_limit":
		return fmt.Sprintf("%s: %s (%s)", result.Provider, result.Status, result.ErrorCode)
	default:
		return ""
	}
}
