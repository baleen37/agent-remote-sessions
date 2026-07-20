package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

const SocketName = "ars-v1"

type Status string

const (
	StatusOK          Status = "ok"
	StatusUnavailable Status = "unavailable"
	StatusFailed      Status = "failed"
)

type Report struct {
	Status    Status
	ErrorCode string
}

func Key(provider, nativeID string) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%d:%s%d:%s", len(provider), provider, len(nativeID), nativeID)
	return "ars-" + hex.EncodeToString(hash.Sum(nil))
}

func Inspect(ctx context.Context, runner Runner, candidates []session.Candidate) (map[string]session.Runtime, Report) {
	runtimes := make(map[string]session.Runtime, len(candidates))
	for _, candidate := range candidates {
		runtimes[Key(string(candidate.Provider), candidate.NativeID)] = session.Runtime{State: session.RuntimeSaved}
	}

	if runner == nil {
		return runtimes, failedReport()
	}
	output, err := runner.Output(ctx, inspectCommand())
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return runtimes, Report{Status: StatusUnavailable, ErrorCode: "tmux_unavailable"}
		}
		var exitError interface{ ExitCode() int }
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return runtimes, Report{Status: StatusOK}
		}
		return runtimes, failedReport()
	}
	if len(output) > maxInspectOutputBytes {
		return runtimes, failedReport()
	}

	parsed, ok := parseRuntimeRows(output, runtimes)
	if !ok {
		return runtimes, failedReport()
	}
	for key, value := range parsed {
		runtimes[key] = value
	}
	return runtimes, Report{Status: StatusOK}
}

func inspectCommand() Command {
	return Command{
		Name: "tmux",
		Args: []string{"-L", SocketName, "-f", "/dev/null", "list-sessions", "-F",
			"#{session_name}\t#{session_attached}\t#{session_created}"},
		Env: []string{"TMUX=", "TMUX_PANE=", "TMUX_TMPDIR=/tmp"},
	}
}

func parseRuntimeRows(output []byte, known map[string]session.Runtime) (map[string]session.Runtime, bool) {
	parsed := make(map[string]session.Runtime)
	seen := make(map[string]struct{})
	if len(output) == 0 {
		return parsed, true
	}

	text := strings.TrimSuffix(string(output), "\n")
	for _, row := range strings.Split(text, "\n") {
		fields := strings.Split(row, "\t")
		if len(fields) != 3 || fields[0] == "" {
			return nil, false
		}
		if _, duplicate := seen[fields[0]]; duplicate {
			return nil, false
		}
		seen[fields[0]] = struct{}{}

		clients, ok := parseNonNegativeInt(fields[1])
		if !ok {
			return nil, false
		}
		created, ok := parsePositiveInt64(fields[2])
		if !ok {
			return nil, false
		}
		if _, owned := known[fields[0]]; !owned {
			continue
		}

		state := session.RuntimeRunning
		if clients > 0 {
			state = session.RuntimeAttached
		}
		parsed[fields[0]] = session.Runtime{
			State:           state,
			AttachedClients: clients,
			StartedAt:       time.Unix(created, 0).UTC(),
		}
	}
	return parsed, true
}

func parseNonNegativeInt(value string) (int, bool) {
	parsed, ok := parseDecimal(value, strconv.IntSize)
	return int(parsed), ok
}

func parsePositiveInt64(value string) (int64, bool) {
	parsed, ok := parseDecimal(value, 63)
	return int64(parsed), ok && parsed > 0
}

func parseDecimal(value string, bitSize int) (uint64, bool) {
	if value == "" {
		return 0, false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.ParseUint(value, 10, bitSize)
	return parsed, err == nil
}

func failedReport() Report {
	return Report{Status: StatusFailed, ErrorCode: "tmux_failed"}
}
