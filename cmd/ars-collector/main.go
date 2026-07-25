package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"time"

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

	var encoder *protocol.StreamEncoder
	var encodeErr error
	err := provider.DiscoverAllStream(
		ctx,
		home,
		adapters,
		time.Now().Add(-session.RecentWindow),
		func(snapshot provider.Snapshot) error {
			states, report := runtime.Inspect(ctx, runtimeRunner, snapshot.Candidates)
			if encoder == nil {
				encoder, encodeErr = protocol.NewStreamEncoder(stdout, args[0], protocol.DefaultLimits())
				if encodeErr != nil {
					return encodeErr
				}
			}
			encodeErr = encoder.Encode(protocol.Snapshot{
				Phase:      snapshot.Phase,
				Discovered: combineRuntime(snapshot.Candidates, states),
				Results:    snapshot.Results,
				Report:     report,
			})
			if encodeErr != nil {
				return encodeErr
			}
			if snapshot.Phase == provider.PhaseComplete {
				for _, result := range snapshot.Results {
					if diagnostic := providerDiagnostic(result); diagnostic != "" {
						fmt.Fprintln(stderr, diagnostic)
					}
				}
			}
			return nil
		},
	)
	if err != nil {
		if encodeErr != nil {
			fmt.Fprintln(stderr, "ars-collector: encode failed")
		} else {
			fmt.Fprintln(stderr, "ars-collector: provider discovery failed")
		}
		return 1
	}
	if encoder == nil || encoder.Close() != nil {
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
