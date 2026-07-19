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
	Type      string           `json:"type"`
	Provider  session.Provider `json:"provider"`
	NativeID  string           `json:"native_id"`
	UpdatedAt time.Time        `json:"updated_at"`
	CWD       string           `json:"cwd"`
	Title     string           `json:"title"`
}

type summaryFrame struct {
	Type      string           `json:"type"`
	Provider  session.Provider `json:"provider"`
	Status    provider.Status  `json:"status"`
	Seen      int              `json:"seen"`
	Skipped   int              `json:"skipped"`
	ErrorCode string           `json:"error_code,omitempty"`
}

func Encode(output io.Writer, nonce string, candidates []session.Candidate, results []provider.Result) error {
	if output == nil {
		return fmt.Errorf("protocol output is nil")
	}
	if err := validateNonce(nonce); err != nil {
		return err
	}
	limits := DefaultLimits()
	if len(candidates) > limits.Sessions {
		return fmt.Errorf("session count exceeds limit")
	}
	for _, candidate := range candidates {
		if err := session.ValidateCandidate(candidate); err != nil {
			return fmt.Errorf("invalid session candidate: %w", err)
		}
	}
	if err := validateResults(results); err != nil {
		return err
	}
	if err := validateCandidateSummaries(candidates, results); err != nil {
		return err
	}

	encoder := boundedEncoder{output: output, limits: limits}
	if err := encoder.writeLine([]byte("ARS/1 BEGIN " + nonce)); err != nil {
		return err
	}
	for _, candidate := range candidates {
		frame := sessionFrame{
			Type:      "session",
			Provider:  candidate.Provider,
			NativeID:  candidate.NativeID,
			UpdatedAt: candidate.UpdatedAt,
			CWD:       candidate.CWD,
			Title:     candidate.Title,
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
	return encoder.writeLine([]byte(fmt.Sprintf("ARS/1 END %s %d", nonce, len(candidates))))
}

func Decode(input io.Reader, nonce string, limits Limits) ([]session.Candidate, []provider.Result, error) {
	if input == nil {
		return nil, nil, fmt.Errorf("protocol input is nil")
	}
	if err := validateNonce(nonce); err != nil {
		return nil, nil, err
	}
	if err := validateLimits(limits); err != nil {
		return nil, nil, err
	}

	limited := &io.LimitedReader{R: input, N: limits.TotalBytes + 1}
	reader := bufio.NewReaderSize(limited, limits.LineBytes+1)
	startupBytes := int64(0)
	for {
		line, consumed, err := readLine(reader, limited, limits)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, nil, fmt.Errorf("missing protocol begin")
			}
			return nil, nil, err
		}
		if strings.HasPrefix(string(line), "ARS/") {
			if err := parseBegin(line, nonce); err != nil {
				return nil, nil, err
			}
			break
		}
		startupBytes += int64(consumed)
		if startupBytes > limits.StartupBytes {
			return nil, nil, fmt.Errorf("startup output exceeds limit")
		}
	}

	candidates := make([]session.Candidate, 0)
	results := make([]provider.Result, 0, 2)
	summaries := make(map[session.Provider]struct{}, 2)
	for {
		line, _, err := readLine(reader, limited, limits)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, nil, fmt.Errorf("missing protocol end")
			}
			return nil, nil, err
		}
		if strings.HasPrefix(string(line), "ARS/") {
			count, err := parseEnd(line, nonce)
			if err != nil {
				return nil, nil, err
			}
			if count != len(candidates) {
				return nil, nil, fmt.Errorf("session count mismatch")
			}
			if err := validateDecodedSummaries(summaries); err != nil {
				return nil, nil, err
			}
			if err := validateCandidateSummaries(candidates, results); err != nil {
				return nil, nil, err
			}
			if trailing, _, err := readLine(reader, limited, limits); err == nil || len(trailing) != 0 {
				return nil, nil, fmt.Errorf("trailing protocol output")
			} else if !errors.Is(err, io.EOF) {
				return nil, nil, err
			}
			for i := range results {
				for _, candidate := range candidates {
					if candidate.Provider == results[i].Provider {
						results[i].Sessions = append(results[i].Sessions, candidate)
					}
				}
			}
			return candidates, results, nil
		}

		var header struct {
			Type string `json:"type"`
		}
		if !utf8.Valid(line) {
			return nil, nil, fmt.Errorf("protocol line is not valid UTF-8")
		}
		if err := json.Unmarshal(line, &header); err != nil {
			return nil, nil, fmt.Errorf("invalid protocol frame")
		}
		switch header.Type {
		case "session":
			if len(candidates) >= limits.Sessions {
				return nil, nil, fmt.Errorf("session count exceeds limit")
			}
			var frame sessionFrame
			if err := strictJSON(line, &frame); err != nil {
				return nil, nil, fmt.Errorf("invalid session frame")
			}
			candidate := session.Candidate{
				Provider:  frame.Provider,
				NativeID:  frame.NativeID,
				UpdatedAt: frame.UpdatedAt,
				CWD:       frame.CWD,
				Title:     frame.Title,
			}
			if err := session.ValidateCandidate(candidate); err != nil {
				return nil, nil, fmt.Errorf("invalid session candidate: %w", err)
			}
			candidates = append(candidates, candidate)
		case "summary":
			var frame summaryFrame
			if err := strictJSON(line, &frame); err != nil {
				return nil, nil, fmt.Errorf("invalid summary frame")
			}
			result := provider.Result{
				Provider:  frame.Provider,
				Status:    frame.Status,
				Seen:      frame.Seen,
				Skipped:   frame.Skipped,
				ErrorCode: frame.ErrorCode,
			}
			if _, exists := summaries[result.Provider]; exists {
				return nil, nil, fmt.Errorf("duplicate provider summary")
			}
			if err := validateResult(result); err != nil {
				return nil, nil, err
			}
			summaries[result.Provider] = struct{}{}
			results = append(results, result)
		default:
			return nil, nil, fmt.Errorf("unknown protocol frame type")
		}
	}
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
	if len(fields) == 0 || fields[0] != "ARS/1" {
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
	if len(fields) == 0 || fields[0] != "ARS/1" {
		return 0, fmt.Errorf("unsupported protocol version")
	}
	if len(fields) != 4 || fields[1] != "END" || string(line) != strings.Join(fields, " ") {
		return 0, fmt.Errorf("invalid protocol end")
	}
	if fields[2] != nonce {
		return 0, fmt.Errorf("protocol nonce mismatch")
	}
	count, err := strconv.Atoi(fields[3])
	if err != nil || count < 0 {
		return 0, fmt.Errorf("invalid protocol session count")
	}
	return count, nil
}

func strictJSON(line []byte, target any) error {
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

func validateCandidateSummaries(candidates []session.Candidate, results []provider.Result) error {
	counts := make(map[session.Provider]int, 2)
	for _, candidate := range candidates {
		counts[candidate.Provider]++
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
