package ssh

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/baleen37/agent-remote-sessions/internal/protocol"
	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

const (
	defaultConnectTimeout = 5 * time.Second
	defaultHostTimeout    = 60 * time.Second
	cleanupTimeout        = 5 * time.Second
	probeOutputLimit      = 64 << 10
	stderrOutputLimit     = 64 << 10
)

type CollectorAssets interface {
	ForTarget(goos, goarch string) ([]byte, error)
}

type CollectOptions struct {
	ConnectTimeout time.Duration
	HostTimeout    time.Duration
	ProtocolLimits protocol.Limits
}

func Collect(ctx context.Context, runner Runner, assets CollectorAssets, target string, options CollectOptions) ([]session.Candidate, []provider.Result, error) {
	if runner == nil {
		return nil, nil, fmt.Errorf("SSH runner is nil")
	}
	if assets == nil {
		return nil, nil, fmt.Errorf("collector assets are nil")
	}
	options = withDefaults(options)
	if err := validateOptions(options); err != nil {
		return nil, nil, err
	}

	hostCtx, cancel := context.WithTimeout(ctx, options.HostTimeout)
	defer cancel()

	probeOutput := newBoundedBuffer(probeOutputLimit)
	probeError := newBoundedBuffer(stderrOutputLimit)
	if err := runner.Run(hostCtx, "ssh", collectionSSHArgs(target, options.ConnectTimeout, "uname -s; uname -m"), nil, probeOutput, probeError); err != nil {
		return nil, nil, commandError("SSH target probe", err, probeError)
	}
	if probeOutput.exceeded {
		return nil, nil, fmt.Errorf("SSH target probe stdout exceeds limit")
	}
	goos, goarch, err := parseTarget(probeOutput.Bytes())
	if err != nil {
		return nil, nil, err
	}
	collector, err := assets.ForTarget(goos, goarch)
	if err != nil {
		return nil, nil, fmt.Errorf("collector asset: %w", err)
	}
	nonce, err := newNonce()
	if err != nil {
		return nil, nil, fmt.Errorf("generate collector nonce: %w", err)
	}

	collectorOutput := newBoundedBuffer(options.ProtocolLimits.TotalBytes)
	collectorError := newBoundedBuffer(stderrOutputLimit)
	runErr := runner.Run(
		hostCtx,
		"ssh",
		collectionSSHArgs(target, options.ConnectTimeout, collectorCommand(nonce)),
		bytes.NewReader(collector),
		collectorOutput,
		collectorError,
	)
	tempPath, pathErr := parseTemporaryPath(collectorOutput.Bytes(), nonce)
	if interrupted(runErr, hostCtx, ctx) && pathErr == nil {
		attemptCleanup(runner, target, options.ConnectTimeout, tempPath)
	}
	if runErr != nil {
		return nil, nil, commandError("SSH collector", runErr, collectorError)
	}
	if collectorOutput.exceeded {
		return nil, nil, fmt.Errorf("collector stdout exceeds limit")
	}
	if pathErr != nil {
		return nil, nil, pathErr
	}
	candidates, results, err := protocol.Decode(bytes.NewReader(collectorOutput.Bytes()), nonce, options.ProtocolLimits)
	if err != nil {
		return nil, nil, fmt.Errorf("collector protocol: %w", err)
	}
	return candidates, results, nil
}

func withDefaults(options CollectOptions) CollectOptions {
	if options.ConnectTimeout == 0 {
		options.ConnectTimeout = defaultConnectTimeout
	}
	if options.HostTimeout == 0 {
		options.HostTimeout = defaultHostTimeout
	}
	if options.ProtocolLimits == (protocol.Limits{}) {
		options.ProtocolLimits = protocol.DefaultLimits()
	}
	return options
}

func validateOptions(options CollectOptions) error {
	if options.ConnectTimeout <= 0 {
		return fmt.Errorf("connect timeout must be positive")
	}
	if options.ConnectTimeout > defaultConnectTimeout {
		return fmt.Errorf("connect timeout exceeds hard limit")
	}
	if options.HostTimeout <= 0 {
		return fmt.Errorf("host timeout must be positive")
	}
	if options.HostTimeout > defaultHostTimeout {
		return fmt.Errorf("host timeout exceeds hard limit")
	}
	if options.ProtocolLimits.StartupBytes <= 0 || options.ProtocolLimits.LineBytes <= 0 || options.ProtocolLimits.TotalBytes <= 0 || options.ProtocolLimits.Sessions <= 0 {
		return fmt.Errorf("protocol limits must be positive")
	}
	defaults := protocol.DefaultLimits()
	if options.ProtocolLimits.StartupBytes > defaults.StartupBytes ||
		options.ProtocolLimits.LineBytes > defaults.LineBytes ||
		options.ProtocolLimits.TotalBytes > defaults.TotalBytes ||
		options.ProtocolLimits.Sessions > defaults.Sessions {
		return fmt.Errorf("protocol limits exceed hard limit")
	}
	return nil
}

func collectionSSHArgs(target string, connectTimeout time.Duration, remoteCommand string) []string {
	seconds := int64(math.Ceil(connectTimeout.Seconds()))
	return []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ForwardAgent=no",
		"-o", "ForwardX11=no",
		"-o", "ClearAllForwardings=yes",
		"-o", "ConnectionAttempts=1",
		"-o", "ConnectTimeout=" + strconv.FormatInt(seconds, 10),
		"-o", "StrictHostKeyChecking=yes",
		"--", target,
		remoteCommand,
	}
}

func parseTarget(output []byte) (string, string, error) {
	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	if len(lines) != 2 || lines[0] == "" || lines[1] == "" {
		return "", "", fmt.Errorf("invalid SSH target probe output")
	}
	switch {
	case lines[0] == "Darwin" && lines[1] == "arm64":
		return "darwin", "arm64", nil
	case lines[0] == "Linux" && (lines[1] == "x86_64" || lines[1] == "amd64"):
		return "linux", "amd64", nil
	case lines[0] == "Linux" && (lines[1] == "aarch64" || lines[1] == "arm64"):
		return "linux", "arm64", nil
	default:
		return "", "", fmt.Errorf("unsupported SSH target %s/%s", lines[0], lines[1])
	}
}

func newNonce() (string, error) {
	var nonce [16]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce[:]), nil
}

func collectorCommand(nonce string) string {
	return "set -eu\n" +
		"nonce='" + nonce + "'\n" +
		"umask 077\n" +
		"base=${TMPDIR:-/tmp}\n" +
		"case \"$base\" in /*) ;; *) base=/tmp ;; esac\n" +
		"dir=\"$base/ars-$nonce\"\n" +
		"created=\n" +
		"cleanup() {\n" +
		"  if [ \"$created\" = 1 ]; then\n" +
		"    rm -f -- \"$dir/collector\"\n" +
		"    rmdir -- \"$dir\"\n" +
		"  fi\n" +
		"}\n" +
		"trap cleanup EXIT\n" +
		"trap 'exit 1' HUP INT TERM\n" +
		"mkdir -- \"$dir\"\n" +
		"created=1\n" +
		"printf '%s\\n' \"$dir\"\n" +
		"cat > \"$dir/collector\"\n" +
		"chmod 700 \"$dir/collector\"\n" +
		"\"$dir/collector\" \"$nonce\""
}

func parseTemporaryPath(output []byte, nonce string) (string, error) {
	newline := bytes.IndexByte(output, '\n')
	if newline <= 0 {
		return "", fmt.Errorf("invalid remote temporary path")
	}
	value := string(output[:newline])
	if !utf8.ValidString(value) || len(value) > 4096 || !strings.HasPrefix(value, "/") || path.Clean(value) != value || path.Base(value) != "ars-"+nonce {
		return "", fmt.Errorf("invalid remote temporary path")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("invalid remote temporary path")
		}
	}
	return value, nil
}

func interrupted(runErr error, hostCtx, parentCtx context.Context) bool {
	return runErr != nil && (hostCtx.Err() != nil || parentCtx.Err() != nil || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded))
}

func attemptCleanup(runner Runner, target string, connectTimeout time.Duration, tempPath string) {
	// A power loss or SIGKILL can prevent both traps and this bounded attempt,
	// leaving only this nonce-specific private directory. V1 has no janitor.
	cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	command := "rm -f -- " + quoteRemotePath(tempPath+"/collector") + "; rmdir -- " + quoteRemotePath(tempPath)
	_ = runner.Run(cleanupCtx, "ssh", collectionSSHArgs(target, connectTimeout, command), nil, io.Discard, newBoundedBuffer(stderrOutputLimit))
}

func quoteRemotePath(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func commandError(operation string, err error, stderr *boundedBuffer) error {
	if diagnostic := strings.TrimSpace(stderr.String()); diagnostic != "" {
		return fmt.Errorf("%s failed: %w: %s", operation, err, diagnostic)
	}
	return fmt.Errorf("%s failed: %w", operation, err)
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int64
	written  int64
	exceeded bool
}

func newBoundedBuffer(limit int64) *boundedBuffer {
	return &boundedBuffer{limit: limit}
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	buffer.written += int64(len(data))
	remaining := buffer.limit - int64(buffer.buffer.Len())
	if remaining > 0 {
		keep := int64(len(data))
		if keep > remaining {
			keep = remaining
		}
		_, _ = buffer.buffer.Write(data[:keep])
	}
	if buffer.written > buffer.limit {
		buffer.exceeded = true
	}
	return len(data), nil
}

func (buffer *boundedBuffer) Bytes() []byte {
	return buffer.buffer.Bytes()
}

func (buffer *boundedBuffer) String() string {
	return buffer.buffer.String()
}
