package protocol

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func FuzzDecode(f *testing.F) {
	for _, seed := range fuzzSeeds(f) {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input []byte) {
		candidates, results, err := Decode(bytes.NewReader(input), testNonce, DefaultLimits())
		if err != nil {
			if candidates != nil || results != nil {
				t.Fatalf("Decode() returned data on error: candidates=%#v results=%#v", candidates, results)
			}
			return
		}

		begin := []byte("ARS/1 BEGIN " + testNonce + "\n")
		end := []byte(fmt.Sprintf("ARS/1 END %s %d\n", testNonce, len(candidates)))
		beginAt := bytes.Index(input, begin)
		if beginAt < 0 || !bytes.Equal(input[len(input)-len(end):], end) {
			t.Fatal("Decode() accepted an incomplete envelope")
		}
		if err := successfulDecodeSemantics(candidates, results); err != nil {
			t.Fatalf("Decode() accepted invalid success semantics: %v", err)
		}
	})
}

func TestSuccessfulDecodeSemanticsRejectsImpossibleSummaries(t *testing.T) {
	for _, tt := range impossibleSummaryCases() {
		t.Run(tt.name, func(t *testing.T) {
			results := append([]provider.Result(nil), tt.results...)
			for i := range results {
				for _, candidate := range tt.candidates {
					if candidate.Provider == results[i].Provider {
						results[i].Sessions = append(results[i].Sessions, candidate)
					}
				}
			}
			if err := successfulDecodeSemantics(tt.candidates, results); err == nil {
				t.Fatal("successfulDecodeSemantics() error = nil, want non-nil")
			}
		})
	}
}

func TestSuccessfulDecodeSemanticsAllowsDeduplicatedSeenCount(t *testing.T) {
	candidate := validCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")
	results := []provider.Result{
		{Provider: session.Claude, Sessions: []session.Candidate{candidate}, Status: provider.OK, Seen: 2},
		{Provider: session.Codex, Status: provider.Absent},
	}
	if err := successfulDecodeSemantics([]session.Candidate{candidate}, results); err != nil {
		t.Fatalf("successfulDecodeSemantics() error = %v", err)
	}
}

func successfulDecodeSemantics(candidates []session.Candidate, results []provider.Result) error {
	candidatesByProvider := make(map[session.Provider][]session.Candidate, 2)
	for _, candidate := range candidates {
		if err := session.ValidateCandidate(candidate); err != nil {
			return fmt.Errorf("invalid candidate: %w", err)
		}
		candidatesByProvider[candidate.Provider] = append(candidatesByProvider[candidate.Provider], candidate)
	}
	if len(results) != 2 {
		return fmt.Errorf("got %d summaries, want 2", len(results))
	}

	seenProviders := make(map[session.Provider]bool, 2)
	for _, result := range results {
		if result.Provider != session.Claude && result.Provider != session.Codex {
			return fmt.Errorf("unknown provider %q", result.Provider)
		}
		if seenProviders[result.Provider] {
			return fmt.Errorf("duplicate provider %q", result.Provider)
		}
		seenProviders[result.Provider] = true

		providerCandidates := candidatesByProvider[result.Provider]
		if len(result.Sessions) != len(providerCandidates) {
			return fmt.Errorf("%s result has %d sessions, want %d", result.Provider, len(result.Sessions), len(providerCandidates))
		}
		for i := range providerCandidates {
			if result.Sessions[i] != providerCandidates[i] {
				return fmt.Errorf("%s result session %d differs from candidate", result.Provider, i)
			}
		}
		if result.Seen < 0 || result.Skipped < 0 || result.Skipped > result.Seen {
			return fmt.Errorf("%s has invalid counts", result.Provider)
		}
		if len(providerCandidates) > result.Seen-result.Skipped {
			return fmt.Errorf("%s candidate count exceeds seen minus skipped", result.Provider)
		}

		hasKnownError := result.ErrorCode == "unavailable" || result.ErrorCode == "incompatible" ||
			result.ErrorCode == "corrupt" || result.ErrorCode == "resource_limit"
		switch result.Status {
		case provider.Absent:
			if result.ErrorCode != "" || result.Seen != 0 || result.Skipped != 0 || len(providerCandidates) != 0 {
				return fmt.Errorf("%s has invalid absent summary", result.Provider)
			}
		case provider.OK:
			if result.ErrorCode != "" {
				return fmt.Errorf("%s OK summary has an error", result.Provider)
			}
		case provider.Partial:
			if !hasKnownError || len(providerCandidates) == 0 {
				return fmt.Errorf("%s has invalid partial summary", result.Provider)
			}
		case provider.Error:
			if !hasKnownError || len(providerCandidates) != 0 {
				return fmt.Errorf("%s has invalid error summary", result.Provider)
			}
		default:
			return fmt.Errorf("%s has unknown status %q", result.Provider, result.Status)
		}
	}
	if !seenProviders[session.Claude] || !seenProviders[session.Codex] {
		return fmt.Errorf("missing built-in provider summary")
	}
	return nil
}

func fuzzSeeds(t testing.TB) [][]byte {
	t.Helper()
	limits := DefaultLimits()
	valid := validTranscript(t)
	candidate := validCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")
	line := sessionLine(t, candidate)

	var tooMany bytes.Buffer
	tooMany.WriteString("ARS/1 BEGIN " + testNonce + "\n")
	for range limits.Sessions + 1 {
		tooMany.Write(line)
	}

	candidate.CWD = "/" + strings.Repeat("c", session.MaxCWDBytes-1)
	candidate.Title = strings.Repeat("t", session.MaxTitleBytes)
	largeLine := sessionLine(t, candidate)
	var tooLarge bytes.Buffer
	tooLarge.WriteString("ARS/1 BEGIN " + testNonce + "\n")
	for tooLarge.Len() <= int(limits.TotalBytes) {
		tooLarge.Write(largeLine)
	}

	invalidCandidate := validCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")
	invalidCandidate.CWD = "relative/path"
	seeds := [][]byte{
		valid,
		[]byte("ARS/1 BEGIN ffffffffffffffffffffffffffffffff\n"),
		[]byte("ARS/1 BEGIN\n"),
		[]byte("ARS/2 BEGIN " + testNonce + "\n"),
		[]byte("ARS/1 BEGIN " + testNonce + "\n{\"type\":\"prompt\"}\n"),
		append([]byte("ARS/1 BEGIN "+testNonce+"\n"), 0xff),
		[]byte("ARS/1 BEGIN " + testNonce + "\n" + strings.Repeat("x", limits.LineBytes+1) + "\n"),
		[]byte(strings.Repeat("x\n", int(limits.StartupBytes/2)+1) + "ARS/1 BEGIN " + testNonce + "\n"),
		tooLarge.Bytes(),
		tooMany.Bytes(),
		valid[:bytes.LastIndex(valid, []byte("ARS/1 END"))],
		bytes.Replace(valid, []byte("ARS/1 END "+testNonce+" 2"), []byte("ARS/1 END "+testNonce+" 1"), 1),
		append([]byte("ARS/1 BEGIN "+testNonce+"\n"), sessionLine(t, invalidCandidate)...),
	}
	for _, tt := range impossibleSummaryCases() {
		seeds = append(seeds, rawTranscript(t, tt.candidates, tt.results))
	}
	return seeds
}
