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
	"strconv"
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

func Encode(output io.Writer, nonce string, discovered []session.Discovered, results []provider.Result, report arsruntime.Report) error {
	if output == nil {
		return fmt.Errorf("protocol output is nil")
	}
	if err := validateNonce(nonce); err != nil {
		return err
	}
	limits := DefaultLimits()
	if len(discovered) > limits.Sessions {
		return fmt.Errorf("session count exceeds limit")
	}
	for _, item := range discovered {
		if _, err := session.BindDiscovered("protocol", item); err != nil {
			return fmt.Errorf("invalid discovered session: %w", err)
		}
	}
	if err := validateResults(results); err != nil {
		return err
	}
	if err := validateCandidateSummaries(discovered, results); err != nil {
		return err
	}
	if err := validateRuntimeReport(report); err != nil {
		return err
	}
	if err := validateReportSessions(discovered, report); err != nil {
		return err
	}

	encoder := boundedEncoder{output: output, limits: limits}
	if err := encoder.writeLine([]byte("ARS/2 BEGIN " + nonce)); err != nil {
		return err
	}
	for _, item := range discovered {
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
		if err := encoder.writeJSON(frame); err != nil {
			return err
		}
	}
	for _, result := range results {
		frame := summaryFrame{
			Type:      "summary",
			Provider:  result.Provider,
			Status:    result.Status,
			Seen:      result.Seen,
			Skipped:   result.Skipped,
			ErrorCode: result.ErrorCode,
		}
		if err := encoder.writeJSON(frame); err != nil {
			return err
		}
	}
	if err := encoder.writeJSON(runtimeFrame{Type: "runtime", Status: report.Status, ErrorCode: report.ErrorCode}); err != nil {
		return err
	}
	return encoder.writeLine([]byte(fmt.Sprintf("ARS/2 END %s %d", nonce, len(discovered))))
}

func Decode(input io.Reader, nonce string, limits Limits) ([]session.Discovered, []provider.Result, arsruntime.Report, error) {
	fail := func(err error) ([]session.Discovered, []provider.Result, arsruntime.Report, error) {
		return nil, nil, arsruntime.Report{}, err
	}
	if input == nil {
		return fail(fmt.Errorf("protocol input is nil"))
	}
	if err := validateNonce(nonce); err != nil {
		return fail(err)
	}
	if err := validateLimits(limits); err != nil {
		return fail(err)
	}

	limited := &io.LimitedReader{R: input, N: limits.TotalBytes + 1}
	reader := bufio.NewReaderSize(limited, limits.LineBytes+1)
	startupBytes := int64(0)
	for {
		line, consumed, err := readLine(reader, limited, limits)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return fail(fmt.Errorf("missing protocol begin"))
			}
			return fail(err)
		}
		if strings.HasPrefix(string(line), "ARS/") {
			if err := parseBegin(line, nonce); err != nil {
				return fail(err)
			}
			break
		}
		startupBytes += int64(consumed)
		if startupBytes > limits.StartupBytes {
			return fail(fmt.Errorf("startup output exceeds limit"))
		}
	}

	discovered := make([]session.Discovered, 0)
	results := make([]provider.Result, 0, 2)
	summaries := make(map[session.Provider]struct{}, 2)
	var report arsruntime.Report
	runtimeSeen := false
	for {
		line, _, err := readLine(reader, limited, limits)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return fail(fmt.Errorf("missing protocol end"))
			}
			return fail(err)
		}
		if strings.HasPrefix(string(line), "ARS/") {
			count, err := parseEnd(line, nonce)
			if err != nil {
				return fail(err)
			}
			if count != len(discovered) {
				return fail(fmt.Errorf("session count mismatch"))
			}
			if err := validateDecodedSummaries(summaries); err != nil {
				return fail(err)
			}
			if !runtimeSeen {
				return fail(fmt.Errorf("missing runtime summary"))
			}
			if err := validateReportSessions(discovered, report); err != nil {
				return fail(err)
			}
			if err := validateCandidateSummaries(discovered, results); err != nil {
				return fail(err)
			}
			if trailing, _, err := readLine(reader, limited, limits); err == nil || len(trailing) != 0 {
				return fail(fmt.Errorf("trailing protocol output"))
			} else if !errors.Is(err, io.EOF) {
				return fail(err)
			}
			for i := range results {
				for _, item := range discovered {
					if item.Candidate.Provider == results[i].Provider {
						results[i].Sessions = append(results[i].Sessions, item.Candidate)
					}
				}
			}
			return discovered, results, report, nil
		}

		var header struct {
			Type string `json:"type"`
		}
		if !utf8.Valid(line) {
			return fail(fmt.Errorf("protocol line is not valid UTF-8"))
		}
		if err := json.Unmarshal(line, &header); err != nil {
			return fail(fmt.Errorf("invalid protocol frame"))
		}
		switch header.Type {
		case "session":
			if len(discovered) >= limits.Sessions {
				return fail(fmt.Errorf("session count exceeds limit"))
			}
			var frame sessionFrame
			if err := strictJSON(line, &frame, "type", "provider", "native_id", "updated_at", "cwd", "title", "runtime_state", "attached_clients"); err != nil {
				return fail(fmt.Errorf("invalid session frame"))
			}
			if err := validateRuntimeFrame(frame); err != nil {
				return fail(err)
			}
			item := session.Discovered{Candidate: session.Candidate{
				Provider: frame.Provider, NativeID: frame.NativeID, UpdatedAt: frame.UpdatedAt,
				CWD: frame.CWD, Title: frame.Title,
			}, Runtime: session.Runtime{State: frame.RuntimeState, AttachedClients: frame.AttachedClients}}
			if frame.RuntimeStarted != nil {
				item.Runtime.StartedAt = *frame.RuntimeStarted
			}
			if _, err := session.BindDiscovered("protocol", item); err != nil {
				return fail(fmt.Errorf("invalid discovered session: %w", err))
			}
			discovered = append(discovered, item)
		case "summary":
			var frame summaryFrame
			if err := strictJSON(line, &frame, "type", "provider", "status", "seen", "skipped"); err != nil {
				return fail(fmt.Errorf("invalid summary frame"))
			}
			result := provider.Result{Provider: frame.Provider, Status: frame.Status, Seen: frame.Seen, Skipped: frame.Skipped, ErrorCode: frame.ErrorCode}
			if _, exists := summaries[result.Provider]; exists {
				return fail(fmt.Errorf("duplicate provider summary"))
			}
			if err := validateResult(result); err != nil {
				return fail(err)
			}
			summaries[result.Provider] = struct{}{}
			results = append(results, result)
		case "runtime":
			if runtimeSeen {
				return fail(fmt.Errorf("duplicate runtime summary"))
			}
			var frame runtimeFrame
			if err := strictJSON(line, &frame, "type", "status"); err != nil {
				return fail(fmt.Errorf("invalid runtime frame"))
			}
			report = arsruntime.Report{Status: frame.Status, ErrorCode: frame.ErrorCode}
			if err := validateRuntimeReport(report); err != nil {
				return fail(err)
			}
			runtimeSeen = true
		default:
			return fail(fmt.Errorf("unknown protocol frame type"))
		}
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
	if len(fields) == 0 || fields[0] != "ARS/2" {
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

func parseEnd(line []byte, nonce string) (int, error) {
	fields := strings.Fields(string(line))
	if len(fields) == 0 || fields[0] != "ARS/2" {
		return 0, fmt.Errorf("unsupported protocol version")
	}
	if len(fields) != 4 || fields[1] != "END" || string(line) != strings.Join(fields, " ") {
		return 0, fmt.Errorf("invalid protocol end")
	}
	if fields[2] != nonce {
		return 0, fmt.Errorf("protocol nonce mismatch")
	}
	count, err := strconv.Atoi(fields[3])
	if err != nil || count < 0 || fields[3] != strconv.Itoa(count) {
		return 0, fmt.Errorf("invalid protocol session count")
	}
	return count, nil
}

func strictJSON(line []byte, target any, required ...string) error {
	if err := validateRequiredFields(line, required); err != nil {
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

func validateRequiredFields(line []byte, required []string) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return fmt.Errorf("protocol frame is not an object")
	}
	fields := make(map[string]json.RawMessage)
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
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
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
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
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
