package protocol

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

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
		if len(results) != 2 {
			t.Fatalf("Decode() accepted %d provider summaries, want 2", len(results))
		}
		for _, candidate := range candidates {
			if err := session.ValidateCandidate(candidate); err != nil {
				t.Fatalf("Decode() returned invalid candidate: %v", err)
			}
		}
	})
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
