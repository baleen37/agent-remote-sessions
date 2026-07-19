package protocol

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

const testNonce = "0123456789abcdef0123456789abcdef"

func TestDefaultLimits(t *testing.T) {
	want := Limits{
		StartupBytes: 64 << 10,
		LineBytes:    64 << 10,
		TotalBytes:   16 << 20,
		Sessions:     10_000,
	}
	if got := DefaultLimits(); got != want {
		t.Fatalf("DefaultLimits() = %#v, want %#v", got, want)
	}
}

func TestRoundTrip(t *testing.T) {
	candidates := []session.Candidate{
		validCandidate(session.Claude, "11111111-1111-1111-1111-111111111111"),
		validCandidate(session.Codex, "22222222-2222-2222-2222-222222222222"),
	}
	results := []provider.Result{
		{Provider: session.Claude, Sessions: candidates[:1], Status: provider.Partial, Seen: 3, Skipped: 2, ErrorCode: "corrupt"},
		{Provider: session.Codex, Sessions: candidates[1:], Status: provider.OK, Seen: 1},
	}

	var encoded bytes.Buffer
	if err := Encode(&encoded, testNonce, candidates, results); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	input := append([]byte("remote startup banner\n"), encoded.Bytes()...)
	gotCandidates, gotResults, err := Decode(bytes.NewReader(input), testNonce, DefaultLimits())
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !reflect.DeepEqual(gotCandidates, candidates) {
		t.Fatalf("Decode() candidates = %#v, want %#v", gotCandidates, candidates)
	}
	if !reflect.DeepEqual(gotResults, results) {
		t.Fatalf("Decode() results = %#v, want %#v", gotResults, results)
	}
}

func TestRoundTripAllowsHealthyEmptyOKSummaries(t *testing.T) {
	results := []provider.Result{
		{Provider: session.Claude, Status: provider.OK},
		{Provider: session.Codex, Status: provider.OK, Seen: 1, Skipped: 1},
	}
	var encoded bytes.Buffer
	if err := Encode(&encoded, testNonce, nil, results); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	candidates, gotResults, err := Decode(bytes.NewReader(encoded.Bytes()), testNonce, DefaultLimits())
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(candidates) != 0 || !reflect.DeepEqual(gotResults, results) {
		t.Fatalf("Decode() = (%#v, %#v), want no candidates and %#v", candidates, gotResults, results)
	}
}

func TestRoundTripAllowsDeduplicatedCandidateCounts(t *testing.T) {
	candidate := validCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")
	results := []provider.Result{
		{Provider: session.Claude, Sessions: []session.Candidate{candidate}, Status: provider.OK, Seen: 2},
		{Provider: session.Codex, Status: provider.Absent},
	}
	var encoded bytes.Buffer
	if err := Encode(&encoded, testNonce, []session.Candidate{candidate}, results); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	candidates, gotResults, err := Decode(bytes.NewReader(encoded.Bytes()), testNonce, DefaultLimits())
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !reflect.DeepEqual(candidates, []session.Candidate{candidate}) || !reflect.DeepEqual(gotResults, results) {
		t.Fatalf("Decode() = (%#v, %#v), want deduplicated candidate and summaries", candidates, gotResults)
	}
}

func TestEncodeRejectsImpossibleSummarySessionCombinations(t *testing.T) {
	for _, tt := range impossibleSummaryCases() {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := Encode(&output, testNonce, tt.candidates, tt.results); err == nil {
				t.Fatal("Encode() error = nil, want non-nil")
			}
		})
	}
}

func TestDecodeRejectsImpossibleSummarySessionCombinations(t *testing.T) {
	for _, tt := range impossibleSummaryCases() {
		t.Run(tt.name, func(t *testing.T) {
			assertDecodeFailsClosed(t, rawTranscript(t, tt.candidates, tt.results), DefaultLimits())
		})
	}
}

func TestDecodeRejectsEnvelopeViolations(t *testing.T) {
	valid := validTranscript(t)
	tests := map[string][]byte{
		"wrong BEGIN nonce":   []byte("ARS/1 BEGIN ffffffffffffffffffffffffffffffff\n"),
		"missing BEGIN nonce": []byte("ARS/1 BEGIN\n"),
		"unknown version":     []byte("ARS/2 BEGIN " + testNonce + "\n"),
		"unknown frame":       []byte("ARS/1 BEGIN " + testNonce + "\n{\"type\":\"prompt\",\"text\":\"must not cross\"}\n"),
		"invalid UTF-8":       append([]byte("ARS/1 BEGIN "+testNonce+"\n"), []byte{'{', '"', 't', 'y', 'p', 'e', '"', ':', '"', 0xff, '"', '}', '\n'}...),
		"truncated END":       valid[:bytes.LastIndex(valid, []byte("ARS/1 END"))],
		"wrong END nonce":     bytes.Replace(valid, []byte("ARS/1 END "+testNonce), []byte("ARS/1 END ffffffffffffffffffffffffffffffff"), 1),
		"missing END nonce":   bytes.Replace(valid, []byte("ARS/1 END "+testNonce+" 2"), []byte("ARS/1 END 2"), 1),
		"mismatched count":    bytes.Replace(valid, []byte("ARS/1 END "+testNonce+" 2"), []byte("ARS/1 END "+testNonce+" 1"), 1),
		"trailing output":     append(append([]byte(nil), valid...), []byte("trailing\n")...),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			assertDecodeFailsClosed(t, input, DefaultLimits())
		})
	}
}

func TestDecodeRejectsNonCanonicalLineEndings(t *testing.T) {
	valid := validTranscript(t)
	tests := map[string][]byte{
		"unterminated final END": bytes.TrimSuffix(valid, []byte{'\n'}),
		"CRLF transcript":        bytes.ReplaceAll(valid, []byte{'\n'}, []byte{'\r', '\n'}),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			assertDecodeFailsClosed(t, input, DefaultLimits())
		})
	}
}

func TestDecodeRejectsNonCanonicalEnvelopeSpacing(t *testing.T) {
	valid := validTranscript(t)
	begin := []byte("ARS/1 BEGIN " + testNonce)
	end := []byte("ARS/1 END " + testNonce + " 2")
	tests := map[string][]byte{
		"leading space in BEGIN":  bytes.Replace(valid, begin, append([]byte{' '}, begin...), 1),
		"tab in BEGIN":            bytes.Replace(valid, begin, []byte("ARS/1\tBEGIN\t"+testNonce), 1),
		"double space in BEGIN":   bytes.Replace(valid, begin, []byte("ARS/1  BEGIN "+testNonce), 1),
		"trailing space in BEGIN": bytes.Replace(valid, begin, append(append([]byte(nil), begin...), ' '), 1),
		"leading space in END":    bytes.Replace(valid, end, append([]byte{' '}, end...), 1),
		"tab in END":              bytes.Replace(valid, end, []byte("ARS/1\tEND\t"+testNonce+"\t2"), 1),
		"double space in END":     bytes.Replace(valid, end, []byte("ARS/1  END "+testNonce+" 2"), 1),
		"trailing space in END":   bytes.Replace(valid, end, append(append([]byte(nil), end...), ' '), 1),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			assertDecodeFailsClosed(t, input, DefaultLimits())
		})
	}
}

func TestDecodeRejectsNonCanonicalEndCount(t *testing.T) {
	valid := validTranscript(t)
	empty := rawTranscript(t, nil, []provider.Result{
		{Provider: session.Claude, Status: provider.OK},
		{Provider: session.Codex, Status: provider.OK},
	})
	tests := map[string][]byte{
		"explicit plus": bytes.Replace(valid, []byte("ARS/1 END "+testNonce+" 2"), []byte("ARS/1 END "+testNonce+" +2"), 1),
		"leading zero":  bytes.Replace(valid, []byte("ARS/1 END "+testNonce+" 2"), []byte("ARS/1 END "+testNonce+" 02"), 1),
		"negative zero": bytes.Replace(empty, []byte("ARS/1 END "+testNonce+" 0"), []byte("ARS/1 END "+testNonce+" -0"), 1),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			assertDecodeFailsClosed(t, input, DefaultLimits())
		})
	}
}

func TestDecodeRejectsOverlongLine(t *testing.T) {
	limits := DefaultLimits()
	input := "ARS/1 BEGIN " + testNonce + "\n" + strings.Repeat("x", limits.LineBytes+1) + "\n"
	assertDecodeFailsClosed(t, []byte(input), limits)
}

func TestDecodeRejectsStartupGarbageAboveLimit(t *testing.T) {
	limits := DefaultLimits()
	input := strings.Repeat("x\n", int(limits.StartupBytes/2)+1) + "ARS/1 BEGIN " + testNonce + "\n"
	assertDecodeFailsClosed(t, []byte(input), limits)
}

func TestDecodeAllowsSmallStartupBeforeLargeTranscript(t *testing.T) {
	candidate := validCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")
	candidate.CWD = "/" + strings.Repeat("c", session.MaxCWDBytes-1)
	candidate.Title = strings.Repeat("t", session.MaxTitleBytes)
	candidates := make([]session.Candidate, 20)
	for i := range candidates {
		candidates[i] = candidate
	}
	results := []provider.Result{
		{Provider: session.Claude, Sessions: candidates, Status: provider.OK, Seen: len(candidates)},
		{Provider: session.Codex, Status: provider.Absent},
	}
	var encoded bytes.Buffer
	if err := Encode(&encoded, testNonce, candidates, results); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	input := append([]byte("x\n"), encoded.Bytes()...)
	got, _, err := Decode(bytes.NewReader(input), testNonce, DefaultLimits())
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(got) != len(candidates) {
		t.Fatalf("len(Decode() candidates) = %d, want %d", len(got), len(candidates))
	}
}

func TestDecodeRejectsTotalOutputAboveLimit(t *testing.T) {
	limits := DefaultLimits()
	candidate := validCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")
	candidate.CWD = "/" + strings.Repeat("c", session.MaxCWDBytes-1)
	candidate.Title = strings.Repeat("t", session.MaxTitleBytes)
	line := sessionLine(t, candidate)

	var input bytes.Buffer
	input.WriteString("ARS/1 BEGIN " + testNonce + "\n")
	for input.Len() <= int(limits.TotalBytes) {
		input.Write(line)
	}
	assertDecodeFailsClosed(t, input.Bytes(), limits)
}

func TestDecodeRejectsTooManySessions(t *testing.T) {
	limits := DefaultLimits()
	line := sessionLine(t, validCandidate(session.Claude, "11111111-1111-1111-1111-111111111111"))

	var input bytes.Buffer
	input.WriteString("ARS/1 BEGIN " + testNonce + "\n")
	for range limits.Sessions + 1 {
		input.Write(line)
	}
	assertDecodeFailsClosed(t, input.Bytes(), limits)
}

func TestDecodeRejectsInvalidCandidate(t *testing.T) {
	candidate := validCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")
	candidate.CWD = "relative/path"
	input := append([]byte("ARS/1 BEGIN "+testNonce+"\n"), sessionLine(t, candidate)...)
	assertDecodeFailsClosed(t, input, DefaultLimits())
}

func TestDecodeRejectsInvalidLimits(t *testing.T) {
	limits := DefaultLimits()
	limits.LineBytes = 0
	assertDecodeFailsClosed(t, validTranscript(t), limits)
}

func TestEncodeRejectsShortWrite(t *testing.T) {
	candidates := []session.Candidate{validCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")}
	results := []provider.Result{
		{Provider: session.Claude, Sessions: candidates, Status: provider.OK, Seen: 1},
		{Provider: session.Codex, Status: provider.Absent},
	}
	if err := Encode(shortWriter{}, testNonce, candidates, results); err == nil {
		t.Fatal("Encode() error = nil, want non-nil")
	}
}

func TestEncodeRejectsTotalOutputAboveLimit(t *testing.T) {
	candidate := validCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")
	candidate.CWD = "/" + strings.Repeat("c", session.MaxCWDBytes-1)
	candidate.Title = strings.Repeat("t", session.MaxTitleBytes)
	candidates := make([]session.Candidate, DefaultLimits().Sessions)
	for i := range candidates {
		candidates[i] = candidate
	}
	results := []provider.Result{
		{Provider: session.Claude, Sessions: candidates, Status: provider.OK, Seen: len(candidates)},
		{Provider: session.Codex, Status: provider.Absent},
	}
	var output bytes.Buffer
	if err := Encode(&output, testNonce, candidates, results); err == nil {
		t.Fatal("Encode() error = nil, want non-nil")
	}
	if int64(output.Len()) > DefaultLimits().TotalBytes {
		t.Fatalf("Encode() wrote %d bytes, limit is %d", output.Len(), DefaultLimits().TotalBytes)
	}
}

func validCandidate(name session.Provider, id string) session.Candidate {
	return session.Candidate{
		Provider:  name,
		NativeID:  id,
		UpdatedAt: time.Date(2026, 7, 19, 1, 2, 3, 456, time.UTC),
		CWD:       "/synthetic/project",
		Title:     "Synthetic title",
	}
}

func validTranscript(t testing.TB) []byte {
	t.Helper()
	candidates := []session.Candidate{
		validCandidate(session.Claude, "11111111-1111-1111-1111-111111111111"),
		validCandidate(session.Codex, "22222222-2222-2222-2222-222222222222"),
	}
	results := []provider.Result{
		{Provider: session.Claude, Sessions: candidates[:1], Status: provider.OK, Seen: 1},
		{Provider: session.Codex, Sessions: candidates[1:], Status: provider.OK, Seen: 1},
	}
	var output bytes.Buffer
	if err := Encode(&output, testNonce, candidates, results); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	return output.Bytes()
}

func sessionLine(t testing.TB, candidate session.Candidate) []byte {
	t.Helper()
	frame := map[string]any{
		"type":       "session",
		"provider":   candidate.Provider,
		"native_id":  candidate.NativeID,
		"updated_at": candidate.UpdatedAt,
		"cwd":        candidate.CWD,
		"title":      candidate.Title,
	}
	line, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	return append(line, '\n')
}

func summaryLine(t testing.TB, result provider.Result) []byte {
	t.Helper()
	frame := map[string]any{
		"type":       "summary",
		"provider":   result.Provider,
		"status":     result.Status,
		"seen":       result.Seen,
		"skipped":    result.Skipped,
		"error_code": result.ErrorCode,
	}
	line, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	return append(line, '\n')
}

func rawTranscript(t testing.TB, candidates []session.Candidate, results []provider.Result) []byte {
	t.Helper()
	var output bytes.Buffer
	output.WriteString("ARS/1 BEGIN " + testNonce + "\n")
	for _, candidate := range candidates {
		output.Write(sessionLine(t, candidate))
	}
	for _, result := range results {
		output.Write(summaryLine(t, result))
	}
	output.WriteString("ARS/1 END " + testNonce + " " + strconv.Itoa(len(candidates)) + "\n")
	return output.Bytes()
}

func impossibleSummaryCases() []struct {
	name       string
	candidates []session.Candidate
	results    []provider.Result
} {
	claude := validCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")
	absentCodex := provider.Result{Provider: session.Codex, Status: provider.Absent}
	return []struct {
		name       string
		candidates []session.Candidate
		results    []provider.Result
	}{
		{
			name:       "absent with session",
			candidates: []session.Candidate{claude},
			results:    []provider.Result{{Provider: session.Claude, Status: provider.Absent}, absentCodex},
		},
		{
			name:    "absent with counts",
			results: []provider.Result{{Provider: session.Claude, Status: provider.Absent, Seen: 1, Skipped: 1}, absentCodex},
		},
		{
			name:       "error with session",
			candidates: []session.Candidate{claude},
			results: []provider.Result{
				{Provider: session.Claude, Status: provider.Error, Seen: 1, ErrorCode: "resource_limit"}, absentCodex,
			},
		},
		{
			name: "partial with zero sessions",
			results: []provider.Result{
				{Provider: session.Claude, Status: provider.Partial, Seen: 1, Skipped: 1, ErrorCode: "corrupt"}, absentCodex,
			},
		},
		{
			name:       "candidate count above seen minus skipped",
			candidates: []session.Candidate{claude},
			results: []provider.Result{
				{Provider: session.Claude, Status: provider.OK, Seen: 1, Skipped: 1}, absentCodex,
			},
		},
	}
}

func assertDecodeFailsClosed(t *testing.T, input []byte, limits Limits) {
	t.Helper()
	candidates, results, err := Decode(bytes.NewReader(input), testNonce, limits)
	if err == nil {
		t.Fatal("Decode() error = nil, want non-nil")
	}
	if candidates != nil || results != nil {
		t.Fatalf("Decode() returned data on error: candidates=%#v results=%#v", candidates, results)
	}
}

type shortWriter struct{}

func (shortWriter) Write(value []byte) (int, error) { return len(value) - 1, nil }
