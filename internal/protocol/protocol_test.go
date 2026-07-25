package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/runtime"
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
	discovered := savedDiscovered(candidates)
	if err := Encode(&encoded, testNonce, discovered, results, runtime.Report{Status: runtime.StatusOK}); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	input := append([]byte("remote startup banner\n"), encoded.Bytes()...)
	gotCandidates, gotResults, gotReport, err := Decode(bytes.NewReader(input), testNonce, DefaultLimits())
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !reflect.DeepEqual(gotCandidates, discovered) {
		t.Fatalf("Decode() candidates = %#v, want %#v", gotCandidates, discovered)
	}
	if !reflect.DeepEqual(gotResults, results) {
		t.Fatalf("Decode() results = %#v, want %#v", gotResults, results)
	}
	if gotReport != (runtime.Report{Status: runtime.StatusOK}) {
		t.Fatalf("Decode() runtime report = %#v", gotReport)
	}
}

func TestRoundTripARS3RuntimeState(t *testing.T) {
	discovered := []session.Discovered{
		{Candidate: validCandidate(session.Claude, "11111111-1111-1111-1111-111111111111"), Runtime: session.Runtime{State: session.RuntimeSaved}},
		{Candidate: validCandidate(session.Codex, "22222222-2222-2222-2222-222222222222"), Runtime: session.Runtime{
			State: session.RuntimeAttached, AttachedClients: 1, StartedAt: time.Unix(10, 0).UTC()}},
	}
	results := []provider.Result{
		{Provider: session.Claude, Sessions: []session.Candidate{discovered[0].Candidate}, Status: provider.OK, Seen: 1},
		{Provider: session.Codex, Sessions: []session.Candidate{discovered[1].Candidate}, Status: provider.OK, Seen: 1},
	}
	var encoded bytes.Buffer
	if err := Encode(&encoded, testNonce, discovered, results, runtime.Report{Status: runtime.StatusOK}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded.String(), "ARS/3 BEGIN ") {
		t.Fatalf("protocol = %q", encoded.String())
	}
	got, _, report, err := Decode(&encoded, testNonce, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, discovered) || report.Status != runtime.StatusOK {
		t.Fatalf("decoded = %#v %#v", got, report)
	}
}

func TestRoundTripAllowsHealthyEmptyOKSummaries(t *testing.T) {
	results := []provider.Result{
		{Provider: session.Claude, Status: provider.OK},
		{Provider: session.Codex, Status: provider.OK, Seen: 1, Skipped: 1},
	}
	var encoded bytes.Buffer
	if err := Encode(&encoded, testNonce, nil, results, runtime.Report{Status: runtime.StatusOK}); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	candidates, gotResults, _, err := Decode(bytes.NewReader(encoded.Bytes()), testNonce, DefaultLimits())
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
	if err := Encode(&encoded, testNonce, savedDiscovered([]session.Candidate{candidate}), results, runtime.Report{Status: runtime.StatusOK}); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	candidates, gotResults, _, err := Decode(bytes.NewReader(encoded.Bytes()), testNonce, DefaultLimits())
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !reflect.DeepEqual(candidates, savedDiscovered([]session.Candidate{candidate})) || !reflect.DeepEqual(gotResults, results) {
		t.Fatalf("Decode() = (%#v, %#v), want deduplicated candidate and summaries", candidates, gotResults)
	}
}

func TestEncodeRejectsImpossibleSummarySessionCombinations(t *testing.T) {
	for _, tt := range impossibleSummaryCases() {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := Encode(&output, testNonce, savedDiscovered(tt.candidates), tt.results, runtime.Report{Status: runtime.StatusOK}); err == nil {
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
		"wrong BEGIN nonce":   []byte("ARS/3 BEGIN ffffffffffffffffffffffffffffffff\n"),
		"missing BEGIN nonce": []byte("ARS/3 BEGIN\n"),
		"unknown version":     []byte("ARS/2 BEGIN " + testNonce + "\n"),
		"unknown frame":       []byte("ARS/3 BEGIN " + testNonce + "\n{\"type\":\"prompt\",\"text\":\"must not cross\"}\n"),
		"invalid UTF-8":       append([]byte("ARS/3 BEGIN "+testNonce+"\n"), []byte{'{', '"', 't', 'y', 'p', 'e', '"', ':', '"', 0xff, '"', '}', '\n'}...),
		"truncated END":       valid[:bytes.LastIndex(valid, []byte("ARS/3 END"))],
		"wrong END nonce":     bytes.Replace(valid, []byte("ARS/3 END "+testNonce), []byte("ARS/3 END ffffffffffffffffffffffffffffffff"), 1),
		"missing END nonce":   bytes.Replace(valid, []byte("ARS/3 END "+testNonce), []byte("ARS/3 END"), 1),
		"mismatched count": bytes.Replace(valid,
			[]byte("{\"type\":\"snapshot_end\",\"phase\":\"complete\",\"sessions\":2}"),
			[]byte("{\"type\":\"snapshot_end\",\"phase\":\"complete\",\"sessions\":1}"), 1),
		"trailing output": append(append([]byte(nil), valid...), []byte("trailing\n")...),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			assertDecodeFailsClosed(t, input, DefaultLimits())
		})
	}
}

func TestDecodeRequiresExactlyOneValidRuntimeSummary(t *testing.T) {
	valid := validTranscript(t)
	runtimeOK := []byte("{\"type\":\"runtime\",\"status\":\"ok\"}\n")
	tests := map[string][]byte{
		"missing runtime":   bytes.Replace(valid, runtimeOK, nil, 1),
		"duplicate runtime": bytes.Replace(valid, runtimeOK, append(append([]byte(nil), runtimeOK...), runtimeOK...), 1),
		"invalid report": bytes.Replace(valid, runtimeOK,
			[]byte("{\"type\":\"runtime\",\"status\":\"unavailable\"}\n"), 1),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			assertDecodeFailsClosed(t, input, DefaultLimits())
		})
	}
}

func TestDecodeRejectsInvalidRuntimeFieldCombinations(t *testing.T) {
	valid := validTranscript(t)
	saved := []byte("\"runtime_state\":\"saved\",\"attached_clients\":0")
	tests := map[string][]byte{
		"saved with start": bytes.Replace(valid, saved,
			[]byte("\"runtime_state\":\"saved\",\"attached_clients\":0,\"runtime_started_at\":\"0001-01-01T00:00:00Z\""), 1),
		"saved attached": bytes.Replace(valid, saved,
			[]byte("\"runtime_state\":\"saved\",\"attached_clients\":1"), 1),
		"running without start": bytes.Replace(valid, saved,
			[]byte("\"runtime_state\":\"running\",\"attached_clients\":0"), 1),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			assertDecodeFailsClosed(t, input, DefaultLimits())
		})
	}
}

func TestDecodeRejectsMissingNullAndDuplicateFrameFields(t *testing.T) {
	valid := validTranscript(t)
	empty := rawTranscript(t, nil, []provider.Result{
		{Provider: session.Claude, Status: provider.Absent},
		{Provider: session.Codex, Status: provider.Absent},
	})
	tests := map[string][]byte{
		"missing attached clients": bytes.Replace(valid, []byte(",\"attached_clients\":0"), nil, 1),
		"null attached clients": bytes.Replace(valid, []byte("\"attached_clients\":0"),
			[]byte("\"attached_clients\":null"), 1),
		"missing title":         bytes.Replace(valid, []byte(",\"title\":\"Synthetic title\""), nil, 1),
		"missing summary count": bytes.Replace(empty, []byte(",\"seen\":0"), nil, 1),
		"null summary count":    bytes.Replace(empty, []byte("\"seen\":0"), []byte("\"seen\":null"), 1),
		"duplicate runtime status": bytes.Replace(valid,
			[]byte("{\"type\":\"runtime\",\"status\":\"ok\"}"),
			[]byte("{\"type\":\"runtime\",\"status\":\"ok\",\"status\":\"ok\"}"), 1),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			assertDecodeFailsClosed(t, input, DefaultLimits())
		})
	}
}

func TestDecodeRejectsCaseVariantAndAliasCollisionFields(t *testing.T) {
	valid := validTranscript(t)
	tests := map[string][]byte{
		"session case variant": bytes.Replace(valid,
			[]byte("{\"type\":\"session\",\"provider\":"),
			[]byte("{\"type\":\"session\",\"Provider\":"), 1),
		"session alias collision": bytes.Replace(valid,
			[]byte("\"provider\":\"claude\""),
			[]byte("\"provider\":\"claude\",\"Provider\":\"claude\""), 1),
		"summary case variant": bytes.Replace(valid,
			[]byte("{\"type\":\"summary\",\"provider\":\"claude\",\"status\":"),
			[]byte("{\"type\":\"summary\",\"provider\":\"claude\",\"Status\":"), 1),
		"summary alias collision": bytes.Replace(valid,
			[]byte("{\"type\":\"summary\",\"provider\":\"claude\",\"status\":\"ok\""),
			[]byte("{\"type\":\"summary\",\"provider\":\"claude\",\"status\":\"ok\",\"Status\":\"ok\""), 1),
		"runtime case variant": bytes.Replace(valid,
			[]byte("{\"type\":\"runtime\",\"status\":"),
			[]byte("{\"type\":\"runtime\",\"Status\":"), 1),
		"runtime alias collision": bytes.Replace(valid,
			[]byte("{\"type\":\"runtime\",\"status\":\"ok\"}"),
			[]byte("{\"type\":\"runtime\",\"status\":\"ok\",\"Status\":\"ok\"}"), 1),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			assertDecodeFailsClosed(t, input, DefaultLimits())
		})
	}
}

func TestDecodeRejectsNullOptionalFields(t *testing.T) {
	valid := validTranscript(t)
	tests := map[string][]byte{
		"session runtime start": bytes.Replace(valid,
			[]byte("\"attached_clients\":0}"),
			[]byte("\"attached_clients\":0,\"runtime_started_at\":null}"), 1),
		"summary error code": bytes.Replace(valid,
			[]byte("{\"type\":\"summary\",\"provider\":\"claude\",\"status\":\"ok\",\"seen\":1,\"skipped\":0}"),
			[]byte("{\"type\":\"summary\",\"provider\":\"claude\",\"status\":\"ok\",\"seen\":1,\"skipped\":0,\"error_code\":null}"), 1),
		"runtime error code": bytes.Replace(valid,
			[]byte("{\"type\":\"runtime\",\"status\":\"ok\"}"),
			[]byte("{\"type\":\"runtime\",\"status\":\"ok\",\"error_code\":null}"), 1),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			assertDecodeFailsClosed(t, input, DefaultLimits())
		})
	}
}

func TestProtocolRejectsLiveSessionsWithNonOKRuntimeReport(t *testing.T) {
	item := session.Discovered{
		Candidate: validCandidate(session.Claude, "11111111-1111-1111-1111-111111111111"),
		Runtime: session.Runtime{State: session.RuntimeAttached, AttachedClients: 1,
			StartedAt: time.Unix(10, 0).UTC()},
	}
	results := []provider.Result{
		{Provider: session.Claude, Sessions: []session.Candidate{item.Candidate}, Status: provider.OK, Seen: 1},
		{Provider: session.Codex, Status: provider.Absent},
	}
	badReport := runtime.Report{Status: runtime.StatusFailed, ErrorCode: "tmux_failed"}
	if err := Encode(io.Discard, testNonce, []session.Discovered{item}, results, badReport); err == nil {
		t.Fatal("Encode() error = nil, want invalid report/session combination")
	}

	var encoded bytes.Buffer
	if err := Encode(&encoded, testNonce, []session.Discovered{item}, results, runtime.Report{Status: runtime.StatusOK}); err != nil {
		t.Fatal(err)
	}
	input := bytes.Replace(encoded.Bytes(),
		[]byte("{\"type\":\"runtime\",\"status\":\"ok\"}"),
		[]byte("{\"type\":\"runtime\",\"status\":\"failed\",\"error_code\":\"tmux_failed\"}"), 1)
	assertDecodeFailsClosed(t, input, DefaultLimits())
}

func TestEncodeRejectsInvalidRuntimeReports(t *testing.T) {
	results := []provider.Result{{Provider: session.Claude, Status: provider.Absent}, {Provider: session.Codex, Status: provider.Absent}}
	for _, report := range []runtime.Report{
		{},
		{Status: runtime.StatusOK, ErrorCode: "tmux_failed"},
		{Status: runtime.StatusUnavailable, ErrorCode: "tmux_failed"},
		{Status: runtime.StatusFailed, ErrorCode: "tmux_unavailable"},
	} {
		if err := Encode(io.Discard, testNonce, nil, results, report); err == nil {
			t.Fatalf("Encode(report=%#v) error = nil", report)
		}
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
	begin := []byte("ARS/3 BEGIN " + testNonce)
	end := []byte("ARS/3 END " + testNonce)
	tests := map[string][]byte{
		"leading space in BEGIN":  bytes.Replace(valid, begin, append([]byte{' '}, begin...), 1),
		"tab in BEGIN":            bytes.Replace(valid, begin, []byte("ARS/3\tBEGIN\t"+testNonce), 1),
		"double space in BEGIN":   bytes.Replace(valid, begin, []byte("ARS/3  BEGIN "+testNonce), 1),
		"trailing space in BEGIN": bytes.Replace(valid, begin, append(append([]byte(nil), begin...), ' '), 1),
		"leading space in END":    bytes.Replace(valid, end, append([]byte{' '}, end...), 1),
		"tab in END":              bytes.Replace(valid, end, []byte("ARS/3\tEND\t"+testNonce), 1),
		"double space in END":     bytes.Replace(valid, end, []byte("ARS/3  END "+testNonce), 1),
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
		"explicit plus": bytes.Replace(valid,
			[]byte("\"sessions\":2}"), []byte("\"sessions\":+2}"), 1),
		"leading zero": bytes.Replace(valid,
			[]byte("\"sessions\":2}"), []byte("\"sessions\":02}"), 1),
		"negative zero": bytes.Replace(empty,
			[]byte("\"sessions\":0}"), []byte("\"sessions\":-0}"), 1),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			assertDecodeFailsClosed(t, input, DefaultLimits())
		})
	}
}

func TestDecodeRejectsOverlongLine(t *testing.T) {
	limits := DefaultLimits()
	input := "ARS/3 BEGIN " + testNonce + "\n" + strings.Repeat("x", limits.LineBytes+1) + "\n"
	assertDecodeFailsClosed(t, []byte(input), limits)
}

func TestDecodeRejectsStartupGarbageAboveLimit(t *testing.T) {
	limits := DefaultLimits()
	input := strings.Repeat("x\n", int(limits.StartupBytes/2)+1) + "ARS/3 BEGIN " + testNonce + "\n"
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
	if err := Encode(&encoded, testNonce, savedDiscovered(candidates), results, runtime.Report{Status: runtime.StatusOK}); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	input := append([]byte("x\n"), encoded.Bytes()...)
	got, _, _, err := Decode(bytes.NewReader(input), testNonce, DefaultLimits())
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
	input.WriteString("ARS/3 BEGIN " + testNonce + "\n{\"type\":\"snapshot\",\"phase\":\"complete\"}\n")
	for input.Len() <= int(limits.TotalBytes) {
		input.Write(line)
	}
	assertDecodeFailsClosed(t, input.Bytes(), limits)
}

func TestDecodeRejectsTooManySessions(t *testing.T) {
	limits := DefaultLimits()
	line := sessionLine(t, validCandidate(session.Claude, "11111111-1111-1111-1111-111111111111"))

	var input bytes.Buffer
	input.WriteString("ARS/3 BEGIN " + testNonce + "\n{\"type\":\"snapshot\",\"phase\":\"complete\"}\n")
	for range limits.Sessions + 1 {
		input.Write(line)
	}
	assertDecodeFailsClosed(t, input.Bytes(), limits)
}

func TestDecodeRejectsInvalidCandidate(t *testing.T) {
	candidate := validCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")
	candidate.CWD = "relative/path"
	input := append([]byte("ARS/3 BEGIN "+testNonce+"\n{\"type\":\"snapshot\",\"phase\":\"complete\"}\n"), sessionLine(t, candidate)...)
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
	if err := Encode(shortWriter{}, testNonce, savedDiscovered(candidates), results, runtime.Report{Status: runtime.StatusOK}); err == nil {
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
	if err := Encode(&output, testNonce, savedDiscovered(candidates), results, runtime.Report{Status: runtime.StatusOK}); err == nil {
		t.Fatal("Encode() error = nil, want non-nil")
	}
	if int64(output.Len()) > DefaultLimits().TotalBytes {
		t.Fatalf("Encode() wrote %d bytes, limit is %d", output.Len(), DefaultLimits().TotalBytes)
	}
}

func TestARS3StreamEmitsValidatedRecentBeforeComplete(t *testing.T) {
	recent := recentProtocolSnapshot()
	complete := completeProtocolSnapshot()
	encoded := encodeStream(t, DefaultLimits(), recent, complete)

	var got []Snapshot
	if err := DecodeStream(bytes.NewReader(encoded), testNonce, DefaultLimits(), func(snapshot Snapshot) error {
		got = append(got, snapshot)
		return nil
	}); err != nil {
		t.Fatalf("DecodeStream() error = %v", err)
	}
	if !reflect.DeepEqual(got, []Snapshot{recent, complete}) {
		t.Fatalf("DecodeStream() snapshots = %#v, want %#v", got, []Snapshot{recent, complete})
	}
}

func TestARS3CompleteOnlyWrappersRoundTrip(t *testing.T) {
	complete := completeProtocolSnapshot()
	var encoded bytes.Buffer
	if err := Encode(&encoded, testNonce, complete.Discovered, complete.Results, complete.Report); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if !strings.HasPrefix(encoded.String(), "ARS/3 BEGIN "+testNonce+"\n{\"type\":\"snapshot\",\"phase\":\"complete\"}\n") {
		t.Fatalf("Encode() output = %q", encoded.String())
	}
	discovered, results, report, err := Decode(&encoded, testNonce, DefaultLimits())
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !reflect.DeepEqual(discovered, complete.Discovered) ||
		!reflect.DeepEqual(results, complete.Results) || report != complete.Report {
		t.Fatalf("Decode() = (%#v, %#v, %#v), want %#v", discovered, results, report, complete)
	}
}

func TestARS3StreamRejectsInvalidSnapshotOrdering(t *testing.T) {
	complete := rawSnapshot(t, completeProtocolSnapshot())
	recent := rawSnapshot(t, recentProtocolSnapshot())
	tests := map[string][]byte{
		"complete followed by recent":   rawStream(complete, recent),
		"complete followed by complete": rawStream(complete, complete),
		"duplicate recent":              rawStream(recent, recent, complete),
		"recent after complete":         rawStream(complete, recent, complete),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			assertDecodeStreamFails(t, input, DefaultLimits())
		})
	}
}

func TestARS3StreamRejectsSnapshotSummaryViolations(t *testing.T) {
	recent := rawSnapshot(t, recentProtocolSnapshot())
	complete := rawSnapshot(t, completeProtocolSnapshot())
	claudeSummary := summaryLine(t, completeProtocolSnapshot().Results[0])
	codexSummary := summaryLine(t, completeProtocolSnapshot().Results[1])
	recentEnd := []byte("{\"type\":\"snapshot_end\",\"phase\":\"recent\",\"sessions\":1}\n")
	tests := map[string][]byte{
		"recent summary": rawStream(
			bytes.Replace(recent, recentEnd, append(append([]byte(nil), claudeSummary...), recentEnd...), 1),
			complete,
		),
		"complete missing Claude": rawStream(bytes.Replace(complete, claudeSummary, nil, 1)),
		"complete missing Codex":  rawStream(bytes.Replace(complete, codexSummary, nil, 1)),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			assertDecodeStreamFails(t, input, DefaultLimits())
		})
	}
}

func TestARS3StreamRejectsSnapshotCountMismatch(t *testing.T) {
	input := encodeStream(t, DefaultLimits(), completeProtocolSnapshot())
	input = bytes.Replace(input,
		[]byte("{\"type\":\"snapshot_end\",\"phase\":\"complete\",\"sessions\":2}"),
		[]byte("{\"type\":\"snapshot_end\",\"phase\":\"complete\",\"sessions\":1}"), 1)
	assertDecodeStreamFails(t, input, DefaultLimits())
}

func TestARS3StreamRejectsEnvelopeViolations(t *testing.T) {
	valid := encodeStream(t, DefaultLimits(), completeProtocolSnapshot())
	tests := map[string][]byte{
		"wrong begin nonce": bytes.Replace(valid, []byte("ARS/3 BEGIN "+testNonce),
			[]byte("ARS/3 BEGIN ffffffffffffffffffffffffffffffff"), 1),
		"wrong end nonce": bytes.Replace(valid, []byte("ARS/3 END "+testNonce),
			[]byte("ARS/3 END ffffffffffffffffffffffffffffffff"), 1),
		"non-canonical begin": bytes.Replace(valid, []byte("ARS/3 BEGIN "+testNonce),
			[]byte("ARS/3  BEGIN "+testNonce), 1),
		"non-canonical snapshot": bytes.Replace(valid,
			[]byte("{\"type\":\"snapshot\",\"phase\":\"complete\"}"),
			[]byte("{\"type\":\"snapshot\", \"phase\":\"complete\"}"), 1),
		"trailing bytes": append(append([]byte(nil), valid...), []byte("trailing\n")...),
		"truncated":      valid[:bytes.LastIndex(valid, []byte("ARS/3 END"))],
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			assertDecodeStreamFails(t, input, DefaultLimits())
		})
	}
}

func TestARS3StreamStopsOnCallbackError(t *testing.T) {
	sentinel := errors.New("stop")
	input := encodeStream(t, DefaultLimits(), recentProtocolSnapshot(), completeProtocolSnapshot())
	calls := 0
	err := DecodeStream(bytes.NewReader(input), testNonce, DefaultLimits(), func(Snapshot) error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) || calls != 1 {
		t.Fatalf("DecodeStream() = %v after %d callbacks, want sentinel after one", err, calls)
	}
}

func TestARS3StreamAppliesSessionLimitPerSnapshot(t *testing.T) {
	limits := DefaultLimits()
	limits.Sessions = 1
	valid := encodeStream(t, limits, recentProtocolSnapshot(), oneSessionCompleteProtocolSnapshot())
	if err := DecodeStream(bytes.NewReader(valid), testNonce, limits, func(Snapshot) error { return nil }); err != nil {
		t.Fatalf("DecodeStream() within per-snapshot limit error = %v", err)
	}

	recent := rawSnapshot(t, recentProtocolSnapshot())
	line := sessionLine(t, recentProtocolSnapshot().Discovered[0].Candidate)
	recent = bytes.Replace(recent, []byte("{\"type\":\"snapshot_end\",\"phase\":\"recent\",\"sessions\":1}\n"),
		append(append([]byte(nil), line...), []byte("{\"type\":\"snapshot_end\",\"phase\":\"recent\",\"sessions\":2}\n")...), 1)
	assertDecodeStreamFails(t, rawStream(recent, rawSnapshot(t, oneSessionCompleteProtocolSnapshot())), limits)
}

func TestARS3StreamSharesOneWholeStreamByteLimit(t *testing.T) {
	recent := recentProtocolSnapshot()
	complete := completeProtocolSnapshot()
	valid := encodeStream(t, DefaultLimits(), recent, complete)
	limits := DefaultLimits()
	limits.TotalBytes = int64(len(valid) - len("ARS/3 END "+testNonce+"\n") - 1)
	assertDecodeStreamFails(t, valid, limits)

	var output bytes.Buffer
	encoder, err := NewStreamEncoder(&output, testNonce, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(recent); err != nil {
		t.Fatalf("Encode(recent) error = %v", err)
	}
	if err := encoder.Encode(complete); err == nil {
		t.Fatal("Encode(complete) error = nil, want whole-stream limit error")
	}
	if int64(output.Len()) > limits.TotalBytes {
		t.Fatalf("encoder wrote %d bytes, limit is %d", output.Len(), limits.TotalBytes)
	}
}

func TestARS3StreamRejectsInvalidRuntimeInEitherSnapshot(t *testing.T) {
	valid := encodeStream(t, DefaultLimits(), recentProtocolSnapshot(), completeProtocolSnapshot())
	runtimeOK := []byte("{\"type\":\"runtime\",\"status\":\"ok\"}")
	invalid := []byte("{\"type\":\"runtime\",\"status\":\"failed\"}")
	first := bytes.Replace(valid, runtimeOK, invalid, 1)
	firstRuntime := bytes.Index(valid, runtimeOK)
	secondStart := firstRuntime + len(runtimeOK)
	second := append(append([]byte(nil), valid[:secondStart]...),
		bytes.Replace(valid[secondStart:], runtimeOK, invalid, 1)...)
	tests := map[string][]byte{"recent": first, "complete": second}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			assertDecodeStreamFails(t, input, DefaultLimits())
		})
	}
}

func TestARS3FinalDecodeReturnsNilAfterValidRecentAndBrokenComplete(t *testing.T) {
	input := encodeStream(t, DefaultLimits(), recentProtocolSnapshot(), completeProtocolSnapshot())
	input = input[:bytes.LastIndex(input, []byte("ARS/3 END"))]
	assertDecodeFailsClosed(t, input, DefaultLimits())
}

func TestARS3EncoderRejectsInvalidContentAndStateBeforeSnapshot(t *testing.T) {
	tests := []struct {
		name      string
		snapshots []Snapshot
	}{
		{name: "recent summaries", snapshots: []Snapshot{func() Snapshot {
			value := recentProtocolSnapshot()
			value.Results = completeProtocolSnapshot().Results
			return value
		}()}},
		{name: "complete missing summary", snapshots: []Snapshot{func() Snapshot {
			value := completeProtocolSnapshot()
			value.Results = value.Results[:1]
			return value
		}()}},
		{name: "complete then another", snapshots: []Snapshot{completeProtocolSnapshot(), completeProtocolSnapshot()}},
		{name: "duplicate recent", snapshots: []Snapshot{recentProtocolSnapshot(), recentProtocolSnapshot()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			encoder, err := NewStreamEncoder(&output, testNonce, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			for index, snapshot := range tt.snapshots {
				before := output.Len()
				err = encoder.Encode(snapshot)
				if index == len(tt.snapshots)-1 {
					if err == nil {
						t.Fatal("Encode() error = nil, want non-nil")
					}
					if output.Len() != before {
						t.Fatalf("invalid Encode() wrote %d bytes", output.Len()-before)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestARS3EncoderRejectsUseAfterClose(t *testing.T) {
	var output bytes.Buffer
	encoder, err := NewStreamEncoder(&output, testNonce, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(completeProtocolSnapshot()); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(completeProtocolSnapshot()); err == nil {
		t.Fatal("Encode() after Close() error = nil")
	}
	if err := encoder.Close(); err == nil {
		t.Fatal("second Close() error = nil")
	}
}

func recentProtocolSnapshot() Snapshot {
	candidate := validCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")
	return Snapshot{
		Phase:      provider.PhaseRecent,
		Discovered: savedDiscovered([]session.Candidate{candidate}),
		Report:     runtime.Report{Status: runtime.StatusOK},
	}
}

func completeProtocolSnapshot() Snapshot {
	candidates := []session.Candidate{
		validCandidate(session.Claude, "11111111-1111-1111-1111-111111111111"),
		validCandidate(session.Codex, "22222222-2222-2222-2222-222222222222"),
	}
	return Snapshot{
		Phase:      provider.PhaseComplete,
		Discovered: savedDiscovered(candidates),
		Results: []provider.Result{
			{Provider: session.Claude, Sessions: candidates[:1], Status: provider.OK, Seen: 1},
			{Provider: session.Codex, Sessions: candidates[1:], Status: provider.OK, Seen: 1},
		},
		Report: runtime.Report{Status: runtime.StatusOK},
	}
}

func oneSessionCompleteProtocolSnapshot() Snapshot {
	candidate := validCandidate(session.Claude, "11111111-1111-1111-1111-111111111111")
	return Snapshot{
		Phase:      provider.PhaseComplete,
		Discovered: savedDiscovered([]session.Candidate{candidate}),
		Results: []provider.Result{
			{Provider: session.Claude, Sessions: []session.Candidate{candidate}, Status: provider.OK, Seen: 1},
			{Provider: session.Codex, Status: provider.Absent},
		},
		Report: runtime.Report{Status: runtime.StatusOK},
	}
}

func encodeStream(t testing.TB, limits Limits, snapshots ...Snapshot) []byte {
	t.Helper()
	var output bytes.Buffer
	encoder, err := NewStreamEncoder(&output, testNonce, limits)
	if err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range snapshots {
		if err := encoder.Encode(snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func rawStream(snapshots ...[]byte) []byte {
	var output bytes.Buffer
	output.WriteString("ARS/3 BEGIN " + testNonce + "\n")
	for _, snapshot := range snapshots {
		output.Write(snapshot)
	}
	output.WriteString("ARS/3 END " + testNonce + "\n")
	return output.Bytes()
}

func rawSnapshot(t testing.TB, snapshot Snapshot) []byte {
	t.Helper()
	var output bytes.Buffer
	phase := testPhaseName(snapshot.Phase)
	output.WriteString("{\"type\":\"snapshot\",\"phase\":\"" + phase + "\"}\n")
	for _, item := range snapshot.Discovered {
		output.Write(sessionLine(t, item.Candidate))
	}
	for _, result := range snapshot.Results {
		output.Write(summaryLine(t, result))
	}
	output.Write(runtimeLine(t, snapshot.Report))
	output.WriteString("{\"type\":\"snapshot_end\",\"phase\":\"" + phase + "\",\"sessions\":" +
		strconv.Itoa(len(snapshot.Discovered)) + "}\n")
	return output.Bytes()
}

func testPhaseName(phase provider.Phase) string {
	if phase == provider.PhaseRecent {
		return "recent"
	}
	return "complete"
}

func assertDecodeStreamFails(t *testing.T, input []byte, limits Limits) {
	t.Helper()
	if err := DecodeStream(bytes.NewReader(input), testNonce, limits, func(Snapshot) error { return nil }); err == nil {
		t.Fatal("DecodeStream() error = nil, want non-nil")
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
	if err := Encode(&output, testNonce, savedDiscovered(candidates), results, runtime.Report{Status: runtime.StatusOK}); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	return output.Bytes()
}

func sessionLine(t testing.TB, candidate session.Candidate) []byte {
	t.Helper()
	frame := map[string]any{
		"type":             "session",
		"provider":         candidate.Provider,
		"native_id":        candidate.NativeID,
		"updated_at":       candidate.UpdatedAt,
		"cwd":              candidate.CWD,
		"title":            candidate.Title,
		"runtime_state":    session.RuntimeSaved,
		"attached_clients": 0,
	}
	line, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	return append(line, '\n')
}

func runtimeLine(t testing.TB, report runtime.Report) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type": "runtime", "status": report.Status, "error_code": report.ErrorCode,
	})
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
	return rawStream(rawSnapshot(t, Snapshot{
		Phase:      provider.PhaseComplete,
		Discovered: savedDiscovered(candidates),
		Results:    results,
		Report:     runtime.Report{Status: runtime.StatusOK},
	}))
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
	candidates, results, report, err := Decode(bytes.NewReader(input), testNonce, limits)
	if err == nil {
		t.Fatal("Decode() error = nil, want non-nil")
	}
	if candidates != nil || results != nil || report != (runtime.Report{}) {
		t.Fatalf("Decode() returned data on error: candidates=%#v results=%#v report=%#v", candidates, results, report)
	}
}

func savedDiscovered(candidates []session.Candidate) []session.Discovered {
	discovered := make([]session.Discovered, len(candidates))
	for i, candidate := range candidates {
		discovered[i] = session.Discovered{Candidate: candidate, Runtime: session.Runtime{State: session.RuntimeSaved}}
	}
	return discovered
}

type shortWriter struct{}

func (shortWriter) Write(value []byte) (int, error) { return len(value) - 1, nil }
