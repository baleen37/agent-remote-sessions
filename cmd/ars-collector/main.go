package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/baleen37/agent-remote-sessions/internal/protocol"
	"github.com/baleen37/agent-remote-sessions/internal/provider"
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
	if len(args) != 1 || !validNonce(args[0]) {
		fmt.Fprintln(stderr, "ars-collector: expected one 128-bit hexadecimal nonce")
		return 2
	}
	if !validAdapters(adapters) {
		fmt.Fprintln(stderr, "ars-collector: invalid provider registry")
		return 1
	}

	results := make([]provider.Result, 0, 2)
	candidates := make([]session.Candidate, 0, protocol.DefaultLimits().Sessions)
	for _, adapter := range adapters {
		result := adapter.Discover(ctx, home)
		if result.Provider != adapter.Name() {
			fmt.Fprintln(stderr, "ars-collector: invalid provider result")
			return 1
		}
		var err error
		candidates, err = appendCandidates(candidates, result.Sessions, result.Provider, protocol.DefaultLimits().Sessions)
		if err != nil {
			fmt.Fprintln(stderr, "ars-collector: invalid provider candidates")
			return 1
		}
		if diagnostic := providerDiagnostic(result); diagnostic != "" {
			fmt.Fprintln(stderr, diagnostic)
		}
		results = append(results, result)
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Provider != candidates[j].Provider {
			return providerOrder(candidates[i].Provider) < providerOrder(candidates[j].Provider)
		}
		return candidates[i].NativeID < candidates[j].NativeID
	})
	sort.Slice(results, func(i, j int) bool {
		return providerOrder(results[i].Provider) < providerOrder(results[j].Provider)
	})
	if err := protocol.Encode(stdout, args[0], candidates, results); err != nil {
		fmt.Fprintln(stderr, "ars-collector: encode failed")
		return 1
	}
	return 0
}

func appendCandidates(candidates, additions []session.Candidate, name session.Provider, limit int) ([]session.Candidate, error) {
	for _, candidate := range additions {
		if candidate.Provider != name || session.ValidateCandidate(candidate) != nil {
			return candidates, fmt.Errorf("invalid provider candidate")
		}
		if len(candidates) >= limit {
			return candidates, fmt.Errorf("combined session count exceeds limit")
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func validNonce(nonce string) bool {
	if len(nonce) != 32 {
		return false
	}
	_, err := hex.DecodeString(nonce)
	return err == nil
}

func validAdapters(adapters []provider.Adapter) bool {
	if len(adapters) != 2 {
		return false
	}
	seen := make(map[session.Provider]struct{}, 2)
	for _, adapter := range adapters {
		if adapter == nil || (adapter.Name() != session.Claude && adapter.Name() != session.Codex) {
			return false
		}
		if _, exists := seen[adapter.Name()]; exists {
			return false
		}
		seen[adapter.Name()] = struct{}{}
	}
	return true
}

func providerOrder(name session.Provider) int {
	if name == session.Claude {
		return 0
	}
	return 1
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
