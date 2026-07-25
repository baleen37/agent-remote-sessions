package protocol

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/baleen37/agent-remote-sessions/internal/provider"
	arsruntime "github.com/baleen37/agent-remote-sessions/internal/runtime"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

type Limits struct {
	StartupBytes int64
	LineBytes    int
	TotalBytes   int64
	Sessions     int
}

func DefaultLimits() Limits {
	return Limits{
		StartupBytes: 64 << 10,
		LineBytes:    64 << 10,
		TotalBytes:   16 << 20,
		Sessions:     10_000,
	}
}

type sessionFrame struct {
	Type            string               `json:"type"`
	Provider        session.Provider     `json:"provider"`
	NativeID        string               `json:"native_id"`
	UpdatedAt       time.Time            `json:"updated_at"`
	CWD             string               `json:"cwd"`
	Title           string               `json:"title"`
	RuntimeState    session.RuntimeState `json:"runtime_state"`
	AttachedClients int                  `json:"attached_clients"`
	RuntimeStarted  *time.Time           `json:"runtime_started_at,omitempty"`
}

type summaryFrame struct {
	Type      string           `json:"type"`
	Provider  session.Provider `json:"provider"`
	Status    provider.Status  `json:"status"`
	Seen      int              `json:"seen"`
	Skipped   int              `json:"skipped"`
	ErrorCode string           `json:"error_code,omitempty"`
}

type runtimeFrame struct {
	Type      string            `json:"type"`
	Status    arsruntime.Status `json:"status"`
	ErrorCode string            `json:"error_code,omitempty"`
}

type snapshotFrame struct {
	Type  string `json:"type"`
	Phase string `json:"phase"`
}

type snapshotEndFrame struct {
	Type     string `json:"type"`
	Phase    string `json:"phase"`
	Sessions int    `json:"sessions"`
}

type Snapshot struct {
	Phase      provider.Phase
	Discovered []session.Discovered
	Results    []provider.Result
	Report     arsruntime.Report
}

type StreamEncoder struct {
	encoder boundedEncoder
	nonce   string
	phase   provider.Phase
	closed  bool
	failed  bool
}

func Encode(output io.Writer, nonce string, discovered []session.Discovered, results []provider.Result, report arsruntime.Report) error {
	encoder, err := NewStreamEncoder(output, nonce, DefaultLimits())
	if err != nil {
		return err
	}
	if err := encoder.Encode(Snapshot{
		Phase: provider.PhaseComplete, Discovered: discovered, Results: results, Report: report,
	}); err != nil {
		return err
	}
	return encoder.Close()
}

func NewStreamEncoder(output io.Writer, nonce string, limits Limits) (*StreamEncoder, error) {
	if output == nil {
		return nil, fmt.Errorf("protocol output is nil")
	}
	if err := validateNonce(nonce); err != nil {
		return nil, err
	}
	if err := validateLimits(limits); err != nil {
		return nil, err
	}
	encoder := &StreamEncoder{
		encoder: boundedEncoder{output: output, limits: limits},
		nonce:   nonce,
	}
	if err := encoder.encoder.writeLine([]byte("ARS/3 BEGIN " + nonce)); err != nil {
		return nil, err
	}
	return encoder, nil
}

func (encoder *StreamEncoder) Encode(snapshot Snapshot) error {
	if encoder == nil {
		return fmt.Errorf("protocol encoder is nil")
	}
	if encoder.closed {
		return fmt.Errorf("protocol encoder is closed")
	}
	if encoder.failed {
		return fmt.Errorf("protocol encoder has failed")
	}
	if err := validateSnapshotOrder(encoder.phase, snapshot.Phase); err != nil {
		return err
	}
	if err := validateSnapshot(snapshot, encoder.encoder.limits); err != nil {
		return err
	}

	phase := phaseName(snapshot.Phase)
	if err := encoder.encoder.writeJSON(snapshotFrame{Type: "snapshot", Phase: phase}); err != nil {
		encoder.failed = true
		return err
	}
	for _, item := range snapshot.Discovered {
		if err := encoder.encoder.writeJSON(newSessionFrame(item)); err != nil {
			encoder.failed = true
			return err
		}
	}
	for _, result := range snapshot.Results {
		frame := summaryFrame{
			Type:      "summary",
			Provider:  result.Provider,
			Status:    result.Status,
			Seen:      result.Seen,
			Skipped:   result.Skipped,
			ErrorCode: result.ErrorCode,
		}
		if err := encoder.encoder.writeJSON(frame); err != nil {
			encoder.failed = true
			return err
		}
	}
	if err := encoder.encoder.writeJSON(runtimeFrame{
		Type: "runtime", Status: snapshot.Report.Status, ErrorCode: snapshot.Report.ErrorCode,
	}); err != nil {
		encoder.failed = true
		return err
	}
	if err := encoder.encoder.writeJSON(snapshotEndFrame{
		Type: "snapshot_end", Phase: phase, Sessions: len(snapshot.Discovered),
	}); err != nil {
		encoder.failed = true
		return err
	}
	encoder.phase = snapshot.Phase
	return nil
}

func (encoder *StreamEncoder) Close() error {
	if encoder == nil {
		return fmt.Errorf("protocol encoder is nil")
	}
	if encoder.closed {
		return fmt.Errorf("protocol encoder is closed")
	}
	if encoder.failed {
		return fmt.Errorf("protocol encoder has failed")
	}
	if encoder.phase != provider.PhaseComplete {
		return fmt.Errorf("protocol stream is incomplete")
	}
	if err := encoder.encoder.writeLine([]byte("ARS/3 END " + encoder.nonce)); err != nil {
		encoder.failed = true
		return err
	}
	encoder.closed = true
	return nil
}

func Decode(input io.Reader, nonce string, limits Limits) ([]session.Discovered, []provider.Result, arsruntime.Report, error) {
	fail := func(err error) ([]session.Discovered, []provider.Result, arsruntime.Report, error) {
		return nil, nil, arsruntime.Report{}, err
	}
	var complete Snapshot
	err := DecodeStream(input, nonce, limits, func(snapshot Snapshot) error {
		if snapshot.Phase == provider.PhaseComplete {
			complete = snapshot
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	return complete.Discovered, complete.Results, complete.Report, nil
}

func DecodeStream(input io.Reader, nonce string, limits Limits, emit func(Snapshot) error) error {
	if input == nil {
		return fmt.Errorf("protocol input is nil")
	}
	if emit == nil {
		return fmt.Errorf("protocol emit callback is nil")
	}
	if err := validateNonce(nonce); err != nil {
		return err
	}
	if err := validateLimits(limits); err != nil {
		return err
	}
	limited := &io.LimitedReader{R: input, N: limits.TotalBytes + 1}
	reader := bufio.NewReaderSize(limited, limits.LineBytes+1)
	startupBytes := int64(0)
	for {
		line, consumed, err := readLine(reader, limited, limits)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("missing protocol begin")
			}
			return err
		}
		if strings.HasPrefix(string(line), "ARS/") {
			if err := parseBegin(line, nonce); err != nil {
				return err
			}
			break
		}
		startupBytes += int64(consumed)
		if startupBytes > limits.StartupBytes {
			return fmt.Errorf("startup output exceeds limit")
		}
	}

	var phase provider.Phase
	for {
		line, _, err := readLine(reader, limited, limits)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("missing protocol end")
			}
			return err
		}
		if strings.HasPrefix(string(line), "ARS/") {
			if err := parseEnd(line, nonce); err != nil {
				return err
			}
			if phase != provider.PhaseComplete {
				return fmt.Errorf("protocol stream is incomplete")
			}
			if trailing, _, err := readLine(reader, limited, limits); err == nil || len(trailing) != 0 {
				return fmt.Errorf("trailing protocol output")
			} else if !errors.Is(err, io.EOF) {
				return err
			}
			return nil
		}
		next, err := parseSnapshotFrame(line)
		if err != nil {
			return err
		}
		if err := validateSnapshotOrder(phase, next); err != nil {
			return err
		}
		snapshot, err := decodeSnapshot(reader, limited, limits, next)
		if err != nil {
			return err
		}
		if err := emit(snapshot); err != nil {
			return err
		}
		phase = next
	}
}

func decodeSnapshot(reader *bufio.Reader, limited *io.LimitedReader, limits Limits, phase provider.Phase) (Snapshot, error) {
	snapshot := Snapshot{Phase: phase}
	summaries := make(map[session.Provider]struct{}, 2)
	runtimeSeen := false
	stage := 0
	for {
		line, _, err := readLine(reader, limited, limits)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return Snapshot{}, fmt.Errorf("missing snapshot end")
			}
			return Snapshot{}, err
		}
		if strings.HasPrefix(string(line), "ARS/") {
			return Snapshot{}, fmt.Errorf("missing snapshot end")
		}
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &header); err != nil {
			return Snapshot{}, fmt.Errorf("invalid protocol frame")
		}
		switch header.Type {
		case "session":
			if stage != 0 {
				return Snapshot{}, fmt.Errorf("session frame is out of order")
			}
			if len(snapshot.Discovered) >= limits.Sessions {
				return Snapshot{}, fmt.Errorf("session count exceeds limit")
			}
			item, err := decodeSessionFrame(line)
			if err != nil {
				return Snapshot{}, err
			}
			snapshot.Discovered = append(snapshot.Discovered, item)
		case "summary":
			if phase != provider.PhaseComplete {
				return Snapshot{}, fmt.Errorf("recent snapshot has provider summary")
			}
			if stage > 1 {
				return Snapshot{}, fmt.Errorf("summary frame is out of order")
			}
			stage = 1
			var frame summaryFrame
			if err := strictJSON(line, &frame, "type", "provider", "status", "seen", "skipped"); err != nil {
				return Snapshot{}, fmt.Errorf("invalid summary frame")
			}
			result := provider.Result{
				Provider: frame.Provider, Status: frame.Status, Seen: frame.Seen,
				Skipped: frame.Skipped, ErrorCode: frame.ErrorCode,
			}
			if _, exists := summaries[result.Provider]; exists {
				return Snapshot{}, fmt.Errorf("duplicate provider summary")
			}
			if err := validateResult(result); err != nil {
				return Snapshot{}, err
			}
			summaries[result.Provider] = struct{}{}
			snapshot.Results = append(snapshot.Results, result)
		case "runtime":
			if runtimeSeen || stage > 1 {
				return Snapshot{}, fmt.Errorf("duplicate runtime summary")
			}
			var frame runtimeFrame
			if err := strictJSON(line, &frame, "type", "status"); err != nil {
				return Snapshot{}, fmt.Errorf("invalid runtime frame")
			}
			snapshot.Report = arsruntime.Report{Status: frame.Status, ErrorCode: frame.ErrorCode}
			if err := validateRuntimeReport(snapshot.Report); err != nil {
				return Snapshot{}, err
			}
			runtimeSeen = true
			stage = 2
		case "snapshot_end":
			if !runtimeSeen {
				return Snapshot{}, fmt.Errorf("missing runtime summary")
			}
			endPhase, count, err := parseSnapshotEndFrame(line)
			if err != nil {
				return Snapshot{}, err
			}
			if endPhase != phase {
				return Snapshot{}, fmt.Errorf("snapshot phase mismatch")
			}
			if count != len(snapshot.Discovered) {
				return Snapshot{}, fmt.Errorf("session count mismatch")
			}
			if phase == provider.PhaseComplete {
				if err := validateDecodedSummaries(summaries); err != nil {
					return Snapshot{}, err
				}
			} else if len(summaries) != 0 {
				return Snapshot{}, fmt.Errorf("recent snapshot has provider summary")
			}
			if err := validateReportSessions(snapshot.Discovered, snapshot.Report); err != nil {
				return Snapshot{}, err
			}
			if phase == provider.PhaseComplete {
				if err := validateCandidateSummaries(snapshot.Discovered, snapshot.Results); err != nil {
					return Snapshot{}, err
				}
				populateResultSessions(snapshot.Discovered, snapshot.Results)
			}
			return snapshot, nil
		default:
			return Snapshot{}, fmt.Errorf("unknown protocol frame type")
		}
	}
}

func decodeSessionFrame(line []byte) (session.Discovered, error) {
	var frame sessionFrame
	if err := strictJSON(line, &frame, "type", "provider", "native_id", "updated_at", "cwd", "title", "runtime_state", "attached_clients"); err != nil {
		return session.Discovered{}, fmt.Errorf("invalid session frame")
	}
	if err := validateRuntimeFrame(frame); err != nil {
		return session.Discovered{}, err
	}
	item := session.Discovered{Candidate: session.Candidate{
		Provider: frame.Provider, NativeID: frame.NativeID, UpdatedAt: frame.UpdatedAt,
		CWD: frame.CWD, Title: frame.Title,
	}, Runtime: session.Runtime{State: frame.RuntimeState, AttachedClients: frame.AttachedClients}}
	if frame.RuntimeStarted != nil {
		item.Runtime.StartedAt = *frame.RuntimeStarted
	}
	if _, err := session.BindDiscovered("protocol", item); err != nil {
		return session.Discovered{}, fmt.Errorf("invalid discovered session: %w", err)
	}
	return item, nil
}

func newSessionFrame(item session.Discovered) sessionFrame {
	candidate := item.Candidate
	frame := sessionFrame{
		Type:            "session",
		Provider:        candidate.Provider,
		NativeID:        candidate.NativeID,
		UpdatedAt:       candidate.UpdatedAt,
		CWD:             candidate.CWD,
		Title:           candidate.Title,
		RuntimeState:    item.Runtime.State,
		AttachedClients: item.Runtime.AttachedClients,
	}
	if !item.Runtime.StartedAt.IsZero() {
		startedAt := item.Runtime.StartedAt
		frame.RuntimeStarted = &startedAt
	}
	return frame
}

func parseSnapshotFrame(line []byte) (provider.Phase, error) {
	var frame snapshotFrame
	if err := strictJSON(line, &frame, "type", "phase"); err != nil || frame.Type != "snapshot" {
		return 0, fmt.Errorf("invalid snapshot frame")
	}
	phase, err := parsePhase(frame.Phase)
	if err != nil {
		return 0, err
	}
	canonical, _ := json.Marshal(snapshotFrame{Type: "snapshot", Phase: frame.Phase})
	if !bytes.Equal(line, canonical) {
		return 0, fmt.Errorf("non-canonical snapshot frame")
	}
	return phase, nil
}

func parseSnapshotEndFrame(line []byte) (provider.Phase, int, error) {
	var frame snapshotEndFrame
	if err := strictJSON(line, &frame, "type", "phase", "sessions"); err != nil || frame.Type != "snapshot_end" {
		return 0, 0, fmt.Errorf("invalid snapshot end frame")
	}
	phase, err := parsePhase(frame.Phase)
	if err != nil {
		return 0, 0, err
	}
	if frame.Sessions < 0 {
		return 0, 0, fmt.Errorf("invalid snapshot session count")
	}
	canonical, _ := json.Marshal(snapshotEndFrame{
		Type: "snapshot_end", Phase: frame.Phase, Sessions: frame.Sessions,
	})
	if !bytes.Equal(line, canonical) {
		return 0, 0, fmt.Errorf("non-canonical snapshot end frame")
	}
	return phase, frame.Sessions, nil
}

func validateSnapshotOrder(previous, next provider.Phase) error {
	switch previous {
	case 0:
		if next == provider.PhaseRecent || next == provider.PhaseComplete {
			return nil
		}
	case provider.PhaseRecent:
		if next == provider.PhaseComplete {
			return nil
		}
	case provider.PhaseComplete:
	}
	return fmt.Errorf("invalid snapshot phase order")
}

func validateSnapshot(snapshot Snapshot, limits Limits) error {
	if snapshot.Phase != provider.PhaseRecent && snapshot.Phase != provider.PhaseComplete {
		return fmt.Errorf("invalid snapshot phase")
	}
	if len(snapshot.Discovered) > limits.Sessions {
		return fmt.Errorf("session count exceeds limit")
	}
	for _, item := range snapshot.Discovered {
		if _, err := session.BindDiscovered("protocol", item); err != nil {
			return fmt.Errorf("invalid discovered session: %w", err)
		}
	}
	if snapshot.Phase == provider.PhaseRecent {
		if len(snapshot.Results) != 0 {
			return fmt.Errorf("recent snapshot has provider summaries")
		}
	} else {
		if err := validateResults(snapshot.Results); err != nil {
			return err
		}
		if err := validateCandidateSummaries(snapshot.Discovered, snapshot.Results); err != nil {
			return err
		}
	}
	if err := validateRuntimeReport(snapshot.Report); err != nil {
		return err
	}
	return validateReportSessions(snapshot.Discovered, snapshot.Report)
}

func populateResultSessions(discovered []session.Discovered, results []provider.Result) {
	for i := range results {
		for _, item := range discovered {
			if item.Candidate.Provider == results[i].Provider {
				results[i].Sessions = append(results[i].Sessions, item.Candidate)
			}
		}
	}
}

func phaseName(phase provider.Phase) string {
	if phase == provider.PhaseRecent {
		return "recent"
	}
	return "complete"
}

func parsePhase(value string) (provider.Phase, error) {
	switch value {
	case "recent":
		return provider.PhaseRecent, nil
	case "complete":
		return provider.PhaseComplete, nil
	default:
		return 0, fmt.Errorf("invalid snapshot phase")
	}
}

func validateRuntimeFrame(frame sessionFrame) error {
	switch frame.RuntimeState {
	case session.RuntimeSaved:
		if frame.AttachedClients != 0 || frame.RuntimeStarted != nil {
			return fmt.Errorf("invalid session runtime")
		}
	case session.RuntimeRunning:
		if frame.AttachedClients != 0 || frame.RuntimeStarted == nil || frame.RuntimeStarted.IsZero() {
			return fmt.Errorf("invalid session runtime")
		}
	case session.RuntimeAttached:
		if frame.AttachedClients <= 0 || frame.RuntimeStarted == nil || frame.RuntimeStarted.IsZero() {
			return fmt.Errorf("invalid session runtime")
		}
	default:
		return fmt.Errorf("invalid session runtime")
	}
	return nil
}

type boundedEncoder struct {
	output io.Writer
	limits Limits
	total  int64
}

func (encoder *boundedEncoder) writeJSON(value any) error {
	line, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode protocol frame: %w", err)
	}
	return encoder.writeLine(line)
}

func (encoder *boundedEncoder) writeLine(line []byte) error {
	if !utf8.Valid(line) {
		return fmt.Errorf("protocol line is not valid UTF-8")
	}
	if len(line) > encoder.limits.LineBytes {
		return fmt.Errorf("protocol line exceeds limit")
	}
	lineBytes := int64(len(line) + 1)
	if lineBytes > encoder.limits.TotalBytes-encoder.total {
		return fmt.Errorf("protocol output exceeds limit")
	}
	encoded := append(line, '\n')
	written, err := encoder.output.Write(encoded)
	if err != nil {
		return fmt.Errorf("write protocol output: %w", err)
	}
	if written != len(encoded) {
		return fmt.Errorf("write protocol output: %w", io.ErrShortWrite)
	}
	encoder.total += lineBytes
	return nil
}

func readLine(reader *bufio.Reader, limited *io.LimitedReader, limits Limits) ([]byte, int, error) {
	line, err := reader.ReadSlice('\n')
	consumed := len(line)
	if limited.N == 0 {
		return nil, consumed, fmt.Errorf("protocol output exceeds limit")
	}
	if errors.Is(err, bufio.ErrBufferFull) {
		return nil, consumed, fmt.Errorf("protocol line exceeds limit")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, consumed, fmt.Errorf("read protocol output: %w", err)
	}
	if errors.Is(err, io.EOF) {
		if len(line) == 0 {
			return nil, consumed, io.EOF
		}
		return nil, consumed, fmt.Errorf("unterminated protocol line")
	}
	line = bytes.TrimSuffix(line, []byte{'\n'})
	if len(line) > 0 && line[len(line)-1] == '\r' {
		return nil, consumed, fmt.Errorf("CRLF protocol line is not allowed")
	}
	if len(line) > limits.LineBytes {
		return nil, consumed, fmt.Errorf("protocol line exceeds limit")
	}
	if !utf8.Valid(line) {
		return nil, consumed, fmt.Errorf("protocol line is not valid UTF-8")
	}
	return line, consumed, nil
}

func parseBegin(line []byte, nonce string) error {
	fields := strings.Fields(string(line))
	if len(fields) == 0 || fields[0] != "ARS/3" {
		return fmt.Errorf("unsupported protocol version")
	}
	if len(fields) != 3 || fields[1] != "BEGIN" || string(line) != strings.Join(fields, " ") {
		return fmt.Errorf("invalid protocol begin")
	}
	if fields[2] != nonce {
		return fmt.Errorf("protocol nonce mismatch")
	}
	return nil
}

func parseEnd(line []byte, nonce string) error {
	fields := strings.Fields(string(line))
	if len(fields) == 0 || fields[0] != "ARS/3" {
		return fmt.Errorf("unsupported protocol version")
	}
	if len(fields) != 3 || fields[1] != "END" || string(line) != strings.Join(fields, " ") {
		return fmt.Errorf("invalid protocol end")
	}
	if fields[2] != nonce {
		return fmt.Errorf("protocol nonce mismatch")
	}
	return nil
}

func strictJSON(line []byte, target any, required ...string) error {
	optional := []string(nil)
	switch target.(type) {
	case *sessionFrame:
		optional = []string{"runtime_started_at"}
	case *summaryFrame, *runtimeFrame:
		optional = []string{"error_code"}
	}
	if err := validateFrameFields(line, required, optional); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("multiple JSON values")
	}
	return nil
}

func validateFrameFields(line []byte, required, optional []string) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return fmt.Errorf("protocol frame is not an object")
	}
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = struct{}{}
	}
	for _, name := range optional {
		allowed[name] = struct{}{}
	}
	fields := make(map[string]json.RawMessage, len(allowed))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok {
			return fmt.Errorf("invalid protocol field name")
		}
		if _, exists := fields[name]; exists {
			return fmt.Errorf("duplicate protocol field")
		}
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("unknown protocol field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("null protocol field")
		}
		fields[name] = value
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return fmt.Errorf("invalid protocol object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("multiple JSON values")
	}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("missing protocol field")
		}
	}
	return nil
}

func validateNonce(nonce string) error {
	if len(nonce) != 32 {
		return fmt.Errorf("nonce must encode 128 bits")
	}
	if _, err := hex.DecodeString(nonce); err != nil {
		return fmt.Errorf("nonce must be hexadecimal")
	}
	return nil
}

func validateLimits(limits Limits) error {
	if limits.StartupBytes <= 0 || limits.LineBytes <= 0 || limits.TotalBytes <= 0 || limits.Sessions <= 0 {
		return fmt.Errorf("protocol limits must be positive")
	}
	if limits.TotalBytes == math.MaxInt64 {
		return fmt.Errorf("protocol total limit is too large")
	}
	return nil
}

func validateResults(results []provider.Result) error {
	if len(results) != 2 {
		return fmt.Errorf("protocol requires two provider summaries")
	}
	seen := make(map[session.Provider]struct{}, 2)
	for _, result := range results {
		if _, exists := seen[result.Provider]; exists {
			return fmt.Errorf("duplicate provider summary")
		}
		if err := validateResult(result); err != nil {
			return err
		}
		seen[result.Provider] = struct{}{}
	}
	return validateDecodedSummaries(seen)
}

func validateDecodedSummaries(summaries map[session.Provider]struct{}) error {
	if len(summaries) != 2 {
		return fmt.Errorf("protocol requires two provider summaries")
	}
	if _, ok := summaries[session.Claude]; !ok {
		return fmt.Errorf("missing Claude provider summary")
	}
	if _, ok := summaries[session.Codex]; !ok {
		return fmt.Errorf("missing Codex provider summary")
	}
	return nil
}

func validateResult(result provider.Result) error {
	if result.Provider != session.Claude && result.Provider != session.Codex {
		return fmt.Errorf("invalid summary provider")
	}
	if result.Seen < 0 || result.Skipped < 0 || result.Skipped > result.Seen {
		return fmt.Errorf("invalid provider counts")
	}
	validError := result.ErrorCode == "unavailable" || result.ErrorCode == "incompatible" ||
		result.ErrorCode == "corrupt" || result.ErrorCode == "resource_limit"
	switch result.Status {
	case provider.Absent, provider.OK:
		if result.ErrorCode != "" {
			return fmt.Errorf("unexpected provider error code")
		}
	case provider.Partial, provider.Error:
		if !validError {
			return fmt.Errorf("invalid provider error code")
		}
	default:
		return fmt.Errorf("invalid provider status")
	}
	return nil
}

func validateCandidateSummaries(discovered []session.Discovered, results []provider.Result) error {
	counts := make(map[session.Provider]int, 2)
	for _, item := range discovered {
		counts[item.Candidate.Provider]++
	}
	for _, result := range results {
		count := counts[result.Provider]
		if count > result.Seen-result.Skipped {
			return fmt.Errorf("provider candidate count exceeds summary")
		}
		switch result.Status {
		case provider.Absent:
			if result.Seen != 0 || result.Skipped != 0 || count != 0 {
				return fmt.Errorf("absent provider has discovery data")
			}
		case provider.Partial:
			if count == 0 {
				return fmt.Errorf("partial provider has no candidates")
			}
		case provider.Error:
			if count != 0 {
				return fmt.Errorf("failed provider has candidates")
			}
		}
	}
	return nil
}

func validateRuntimeReport(report arsruntime.Report) error {
	switch report.Status {
	case arsruntime.StatusOK:
		if report.ErrorCode != "" {
			return fmt.Errorf("unexpected runtime error code")
		}
	case arsruntime.StatusUnavailable:
		if report.ErrorCode != "tmux_unavailable" {
			return fmt.Errorf("invalid runtime error code")
		}
	case arsruntime.StatusFailed:
		if report.ErrorCode != "tmux_failed" {
			return fmt.Errorf("invalid runtime error code")
		}
	default:
		return fmt.Errorf("invalid runtime status")
	}
	return nil
}

func validateReportSessions(discovered []session.Discovered, report arsruntime.Report) error {
	if report.Status == arsruntime.StatusOK {
		return nil
	}
	for _, item := range discovered {
		if item.Runtime.State != session.RuntimeSaved {
			return fmt.Errorf("runtime report conflicts with session state")
		}
	}
	return nil
}
