package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

type ExternalTarget struct {
	Socket      string
	PaneID      string
	PanePID     uint64
	SocketDev   uint64
	SocketInode uint64
	SocketUID   uint64
}

func ResolveExternal(
	ctx context.Context,
	runner Runner,
	name session.Provider,
	nativeID string,
) (ExternalTarget, bool, error) {
	if runner == nil {
		return ExternalTarget{}, false, errors.New("external tmux runner is nil")
	}
	adapter, ok := provider.Lookup(name)
	if !ok {
		return ExternalTarget{}, false, errors.New("unsupported external tmux provider")
	}
	if err := adapter.ValidateID(nativeID); err != nil {
		return ExternalTarget{}, false, err
	}
	output, err := runner.Output(ctx, Command{
		Name: "/bin/sh",
		Args: []string{"-c", ExternalResolverScript(), "ars-external", string(name), nativeID},
		Env:  []string{"TMUX=", "TMUX_PANE="},
	})
	if err != nil {
		return ExternalTarget{}, false, fmt.Errorf("resolve external tmux: %w", err)
	}
	if len(output) > maxInspectOutputBytes {
		return ExternalTarget{}, false, errors.New("external tmux result exceeds limit")
	}
	return parseExternalResult(output)
}

func parseExternalResult(output []byte) (ExternalTarget, bool, error) {
	if len(output) == 0 || output[len(output)-1] != '\n' ||
		strings.Count(string(output), "\n") != 1 {
		return ExternalTarget{}, false, errors.New("invalid external tmux result")
	}
	line := strings.TrimSuffix(string(output), "\n")
	if line == "none" {
		return ExternalTarget{}, false, nil
	}
	fields := strings.Split(line, "\t")
	if len(fields) != 8 || fields[0] != "match" || fields[7] != "socket" {
		return ExternalTarget{}, false, errors.New("invalid external tmux result")
	}
	if !validDecimal(fields[3]) || !validHex(fields[4]) || !validHex(fields[5]) || !validDecimal(fields[6]) {
		return ExternalTarget{}, false, errors.New("invalid external tmux target")
	}
	panePID, paneErr := strconv.ParseUint(fields[3], 10, 64)
	socketDev, devErr := strconv.ParseUint(fields[4], 16, 64)
	socketInode, inodeErr := strconv.ParseUint(fields[5], 16, 64)
	socketUID, uidErr := strconv.ParseUint(fields[6], 10, 64)
	target := ExternalTarget{
		Socket:      fields[1],
		PaneID:      fields[2],
		PanePID:     panePID,
		SocketDev:   socketDev,
		SocketInode: socketInode,
		SocketUID:   socketUID,
	}
	if !filepath.IsAbs(target.Socket) || strings.ContainsAny(target.Socket, "\t\r\n\x00") ||
		!validExternalPaneID(target.PaneID) || paneErr != nil || panePID == 0 ||
		devErr != nil || inodeErr != nil || uidErr != nil {
		return ExternalTarget{}, false, errors.New("invalid external tmux target")
	}
	return target, true, nil
}

func validExternalPaneID(value string) bool {
	if len(value) < 2 || value[0] != '%' {
		return false
	}
	for index := 1; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func validDecimal(value string) bool {
	if value == "" {
		return false
	}
	for index := range len(value) {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func validHex(value string) bool {
	if value == "" {
		return false
	}
	for index := range len(value) {
		if (value[index] < '0' || value[index] > '9') &&
			(value[index] < 'a' || value[index] > 'f') {
			return false
		}
	}
	return true
}

func ExternalResolverScript() string {
	return externalResolverScript
}

const darwinArgvWatchdogScript = `import math
import subprocess
import sys

def fail():
    raise SystemExit(1)

try:
    if len(sys.argv) < 7:
        fail()
    timeout = float(sys.argv[1])
    if not math.isfinite(timeout) or timeout <= 0:
        fail()
    output_path, error_path, candidates_path = sys.argv[2:5]
    with open(error_path, "wb") as error:
        completed = subprocess.run(
            sys.argv[5:],
            stdout=subprocess.PIPE,
            stderr=error,
            timeout=timeout,
            check=True,
        )
    output = completed.stdout
    if output and not output.endswith(b"\n"):
        fail()
    with open(candidates_path, "rb") as candidates:
        allowed = set(candidates.read().splitlines())
    seen = set()
    for candidate in output.splitlines():
        if not candidate or candidate not in allowed or candidate in seen:
            fail()
        seen.add(candidate)
    with open(output_path, "wb") as destination:
        destination.write(output)
except (OSError, ValueError, subprocess.SubprocessError):
    fail()
`

const externalTmuxPythonScript = `import math
import os
import stat
import subprocess
import sys

MAX_OUTPUT = 2 << 20
MAX_ROWS = 16384
FORMAT = "#{pane_id}|#{pane_pid}"

def fail():
    print("ars: invalid external tmux target", file=sys.stderr)
    raise SystemExit(1)

def parse_timeout(value):
    timeout = float(value)
    if not math.isfinite(timeout) or timeout <= 0:
        fail()
    return timeout

def inspect(timeout, socket_path, executable):
    completed = subprocess.run(
        [executable, "-S", socket_path, "-f", "/dev/null", "list-panes", "-a", "-F", FORMAT],
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        timeout=timeout,
        check=True,
    )
    output = completed.stdout
    if len(output) > MAX_OUTPUT or (output and not output.endswith(b"\n")):
        fail()
    seen = set()
    rows = output.splitlines()
    if len(rows) > MAX_ROWS:
        fail()
    for row in rows:
        fields = row.split(b"|")
        if len(fields) != 2 or len(fields[0]) < 2 or fields[0][:1] != b"%" or not fields[0][1:].isdigit():
            fail()
        if not fields[1].isdigit() or fields[1][:1] == b"0" or fields[0] in seen:
            fail()
        seen.add(fields[0])
    return output

def identity(path):
    value = os.lstat(path)
    if not stat.S_ISSOCK(value.st_mode):
        fail()
    return value.st_dev, value.st_ino, value.st_uid

def main(arguments):
    if len(arguments) == 4 and arguments[0] == "inspect":
        sys.stdout.buffer.write(inspect(parse_timeout(arguments[1]), arguments[2], arguments[3]))
        return
    if len(arguments) != 9 or arguments[0] != "attach":
        fail()
    timeout = parse_timeout(arguments[1])
    socket_path, pane_id, pane_pid = arguments[2:5]
    if len(pane_id) < 2 or pane_id[0] != "%" or not pane_id[1:].isdigit():
        fail()
    expected = (int(arguments[5], 16), int(arguments[6], 16), int(arguments[7], 10))
    if identity(socket_path) != expected:
        fail()
    rows = inspect(timeout, socket_path, arguments[8]).splitlines()
    wanted = (pane_id + "|" + pane_pid).encode()
    if wanted not in rows:
        fail()
    if identity(socket_path) != expected:
        fail()
    os.environ.pop("TMUX", None)
    os.environ.pop("TMUX_PANE", None)
    os.execvp(arguments[8], [
        arguments[8], "-S", socket_path, "-f", "/dev/null",
        "attach-session", "-t", pane_id,
    ])

try:
    main(sys.argv[1:])
except (OSError, ValueError, subprocess.SubprocessError):
    fail()
`

const externalResolverScript = `set -eu
LC_ALL=C
export LC_ALL
provider=$1
native_id=$2
uid=$(id -u)
tmp_base=${TMPDIR:-/tmp}
case "$tmp_base" in /*) ;; *) tmp_base=/tmp ;; esac
work=$(mktemp -d "$tmp_base/ars-external.XXXXXX")
cleanup() {
	rm -r -- "$work"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

ps -U "$uid" -o pid=,ppid=,comm= >"$work/processes"
: >"$work/parents"
: >"$work/candidates"
if ! awk -v provider="$provider" -v parents="$work/parents" -v candidates="$work/candidates" '
{
	bytes += length($0) + 1
	pid = $1
	ppid = $2
	comm = $0
	sub(/^[[:space:]]*[0-9]+[[:space:]]+[0-9]+[[:space:]]*/, "", comm)
	if (pid !~ /^[1-9][0-9]*$/ || ppid !~ /^[0-9]+$/ || comm == "") {
		invalid = 1
		next
	}
	if (seen[pid]++) invalid = 1
	print pid "\t" ppid > parents
	name = comm
	sub(/^.*\//, "", name)
	if (name == provider) {
		print pid > candidates
		candidate_count++
	}
}
END {
	close(parents)
	close(candidates)
	if (bytes > 2097152 || NR > 65536 || candidate_count > 256 || invalid) exit 1
}
' "$work/processes"; then
	echo "ars: invalid external process table" >&2
	exit 1
fi

: >"$work/valid-candidates"
if [ -s "$work/candidates" ]; then
platform=$(uname -s)
case "$provider" in
claude) resume_arg=--resume ;;
codex) resume_arg=resume ;;
*) echo "ars: unsupported external process provider" >&2; exit 1 ;;
esac

case "$platform" in
Linux)
	provider_hex=$(printf '%s' "$provider" | od -An -tx1 -v | tr -d ' \n')
	resume_hex=$(printf '%s' "$resume_arg" | od -An -tx1 -v | tr -d ' \n')
	native_id_hex=$(printf '%s' "$native_id" | od -An -tx1 -v | tr -d ' \n')
	while IFS= read -r candidate; do
		if ! head -c 65537 "/proc/$candidate/cmdline" >"$work/argv-raw"; then
			echo "ars: cannot inspect external process argv" >&2
			exit 1
		fi
		argv_bytes=$(wc -c <"$work/argv-raw" | tr -d ' ')
		[ "$argv_bytes" -le 65536 ] || {
			echo "ars: invalid external process argv" >&2
			exit 1
		}
		od -An -tx1 -v "$work/argv-raw" >"$work/argv-hex"
		if awk -v candidate="$candidate" -v provider="$provider_hex" -v resume="$resume_hex" -v native_id="$native_id_hex" '
{
	for (field = 1; field <= NF; field++) {
		token = tolower($field)
		if (length(token) != 2 || token !~ /^[0-9a-f][0-9a-f]$/) invalid = 1
		bytes++
		if (bytes > 65536) oversized = 1
		if (token == "00") {
			argv[++argc] = current
			current = ""
			terminated = 1
		} else {
			current = current token
			terminated = 0
		}
	}
}
END {
	if (invalid || oversized || bytes == 0 || !terminated) exit 42
	if (argc != 3) exit 0
	executable = argv[1]
	provider_length = length(provider)
	executable_length = length(executable)
	provider_match = executable == provider
	if (!provider_match && executable_length > provider_length + 1) {
		provider_match = substr(executable, executable_length - provider_length + 1) == provider &&
			substr(executable, executable_length - provider_length - 1, 2) == "2f"
	}
	if (provider_match && argv[2] == resume && argv[3] == native_id) print candidate
}
' "$work/argv-hex" >>"$work/valid-candidates"; then
			:
		else
			status=$?
			if [ "$status" -eq 42 ]; then
				echo "ars: invalid external process argv" >&2
			else
				echo "ars: cannot inspect external process argv" >&2
			fi
			exit 1
		fi
	done <"$work/candidates"
	;;
Darwin)
	[ -x /usr/bin/osascript ] || {
		echo "ars: cannot inspect external process argv" >&2
		exit 1
	}
	cat >"$work/argv.js" <<'ARS_JXA'
ObjC.import('Foundation')
ObjC.bindFunction('sysctl', ['int', ['^v', 'I', '^v', '^v', '^v', 'Q']])
ObjC.bindFunction('exit', ['void', ['int']])
ObjC.bindFunction('alarm', ['unsigned int', ['unsigned int']])

function fail() {
	$.exit(1)
}

var deadline = Date.now() + 10000

function checkDeadline() {
	if (Date.now() > deadline) fail()
}

function put32(pointer, offset, value) {
	pointer[offset] = value & 255
	pointer[offset + 1] = (value >>> 8) & 255
	pointer[offset + 2] = (value >>> 16) & 255
	pointer[offset + 3] = (value >>> 24) & 255
}

function uint32(pointer, offset) {
	return pointer[offset] + pointer[offset + 1] * 256 +
		pointer[offset + 2] * 65536 + pointer[offset + 3] * 16777216
}

function uint64(pointer) {
	var value = 0
	var factor = 1
	for (var index = 0; index < 8; index++) {
		value += pointer[index] * factor
		factor *= 256
	}
	return value
}

function asciiBytes(value) {
	var result = []
	for (var index = 0; index < value.length; index++) {
		var code = value.charCodeAt(index)
		if (code > 127) fail()
		result.push(code)
	}
	return result
}

function equalBytes(actual, expected) {
	if (actual.length !== expected.length) return false
	for (var index = 0; index < actual.length; index++) {
		if (actual[index] !== expected[index]) return false
	}
	return true
}

function basename(value) {
	var start = 0
	for (var index = 0; index < value.length; index++) {
		if (value[index] === 47) start = index + 1
	}
	return value.slice(start)
}

function candidates(path) {
	var data = $.NSData.dataWithContentsOfFile(path)
	if (!data || data.length > 4096) fail()
	var pointer = data.bytes
	var values = []
	var current = ''
	for (var index = 0; index < data.length; index++) {
		var byte = pointer[index]
		if (byte === 10) {
			if (!/^[1-9][0-9]*$/.test(current)) fail()
			var pid = Number(current)
			if (!Number.isSafeInteger(pid) || pid > 2147483647) fail()
			values.push(pid)
			current = ''
		} else {
			if (byte < 48 || byte > 57) fail()
			current += String.fromCharCode(byte)
		}
	}
	if (current !== '' || values.length > 256) fail()
	return values
}

function processArgv(pid) {
	checkDeadline()
	var mib = $.NSMutableData.dataWithLength(12)
	var mibBytes = mib.mutableBytes
	put32(mibBytes, 0, 1)
	put32(mibBytes, 4, 49)
	put32(mibBytes, 8, pid)
	var output = $.NSMutableData.dataWithLength(65536)
	var size = $.NSMutableData.dataWithLength(8)
	size.mutableBytes[2] = 1
	var dummy = $.NSMutableData.dataWithLength(1)
	if ($.sysctl(mib.mutableBytes, 3, output.mutableBytes, size.mutableBytes, dummy.mutableBytes, 0) !== 0) fail()
	checkDeadline()
	var length = uint64(size.mutableBytes)
	if (!Number.isSafeInteger(length) || length < 5 || length > 65536) fail()
	var pointer = output.bytes
	var argc = uint32(pointer, 0)
	if (argc !== 3) return null
	var offset = 4
	while (offset < length && pointer[offset] !== 0) offset++
	if (offset === length) fail()
	while (offset < length && pointer[offset] === 0) offset++
	if (offset === length) fail()
	var argv = []
	for (var argument = 0; argument < argc; argument++) {
		var value = []
		while (offset < length && pointer[offset] !== 0) value.push(pointer[offset++])
		if (offset === length) fail()
		offset++
		argv.push(value)
	}
	return argv
}

function run(arguments) {
	if (arguments.length !== 4) fail()
	$.alarm(10)
	var provider = asciiBytes(arguments[0])
	var resume = asciiBytes(arguments[1])
	var nativeID = asciiBytes(arguments[2])
	var pids = candidates(arguments[3])
	var matches = ''
	for (var index = 0; index < pids.length; index++) {
		var argv = processArgv(pids[index])
		if (argv !== null && equalBytes(basename(argv[0]), provider) &&
			equalBytes(argv[1], resume) && equalBytes(argv[2], nativeID)) {
			matches += String(pids[index]) + '\n'
		}
	}
	$.alarm(0)
	if (matches !== '') {
		var data = $(matches).dataUsingEncoding($.NSUTF8StringEncoding)
		$.NSFileHandle.fileHandleWithStandardOutput.writeData(data)
	}
}
ARS_JXA
	command -v python3 >/dev/null 2>&1 || {
		echo "ars: cannot inspect external process argv" >&2
		exit 1
	}
	cat >"$work/argv-watchdog.py" <<'PY'
` + darwinArgvWatchdogScript + `PY
	if ! python3 "$work/argv-watchdog.py" 30 "$work/valid-candidates" "$work/argv-error" "$work/candidates" \
		/usr/bin/osascript -l JavaScript "$work/argv.js" "$provider" "$resume_arg" "$native_id" "$work/candidates"; then
		echo "ars: cannot inspect external process argv" >&2
		exit 1
	fi
	;;
*) echo "ars: unsupported external process platform" >&2; exit 1 ;;
esac
fi

if [ ! -s "$work/valid-candidates" ]; then
	printf 'none\n'
	exit 0
fi

match_panes() {
	socket=$1
	socket_dev=$2
	socket_inode=$3
	socket_owner=$4
	if ! python3 "$work/external-tmux.py" inspect 30 "$socket" tmux >"$work/panes"; then
		echo "ars: cannot inspect external tmux socket" >&2
		return 2
	fi
	pane_bytes=$(wc -c <"$work/panes" | tr -d ' ')
	pane_total=$((pane_total + pane_bytes))
	if [ "$pane_total" -gt 2097152 ]; then
		echo "ars: external tmux pane table exceeds limit" >&2
		return 2
	fi
	pane_rows=$((pane_rows + $(awk 'END { print NR }' "$work/panes")))
	if [ "$pane_rows" -gt 16384 ]; then
		echo "ars: external tmux pane row count exceeds limit" >&2
		return 2
	fi
	awk -F '|' -v socket="$socket" -v dev="$socket_dev" -v inode="$socket_inode" -v owner="$socket_owner" \
		'{ print socket "\t" $1 "\t" $2 "\t" dev "\t" inode "\t" owner }' "$work/panes" >>"$work/all-panes"
}

resolve_matches() {
	: >"$work/resolve-status"
	: >"$work/resolve-steps"
	if ! awk -F '\t' -v parents="$work/parents" -v candidates="$work/valid-candidates" -v status="$work/resolve-status" -v steps_file="$work/resolve-steps" -v max_steps="4096" '
FILENAME == parents {
	parent[$1] = $2
	next
}
FILENAME == candidates {
	candidate[$1] = 1
	next
}
{
	targets[$3] = targets[$3] $1 "\t" $2 "\t" $3 "\t" $4 "\t" $5 "\t" $6 "\n"
}
END {
	for (start in candidate) {
		current = start
		depth = 0
		seen[start SUBSEP current] = 1
		while (1) {
			steps++
			if (steps > max_steps) {
				error = "work"
				break
			}
			if (current in targets) {
				count = split(targets[current], matched, "\n")
				for (match_index = 1; match_index < count; match_index++) {
					if (!emitted[matched[match_index]]++) print matched[match_index]
				}
			}
			if (!(current in parent)) break
			next_pid = parent[current]
			depth++
			if (depth > 256) {
				error = "ancestry"
				break
			}
			if (seen[start SUBSEP next_pid]++) {
				error = "cycle"
				break
			}
			current = next_pid
		}
		if (error != "") break
	}
	print steps > steps_file
	close(steps_file)
	if (error != "") {
		print error > status
		close(status)
		exit 1
	}
}
' "$work/parents" "$work/valid-candidates" "$work/all-panes" >>"$work/matches"; then
	if [ -s "$work/resolve-status" ]; then
		reason=$(cat "$work/resolve-status")
		case "$reason" in
		work) echo "ars: external tmux resolver work exceeds limit" >&2 ;;
		ancestry) echo "ars: external process ancestry exceeds limit" >&2 ;;
		cycle) echo "ars: external process ancestry cycle" >&2 ;;
		*) echo "ars: invalid external tmux resolver" >&2 ;;
		esac
	else
		echo "ars: invalid external tmux resolver" >&2
	fi
	return 2
	fi
}

: >"$work/matches"
: >"$work/all-panes"
mkdir "$work/socket-paths"
: >"$work/socket-inventory"
: >"$work/socket-inventory-sockets"
cat >"$work/external-tmux.py" <<'PY'
` + externalTmuxPythonScript + `PY
cat >"$work/socket-inventory.py" <<'PY'
import os
import stat
import sys

def fail():
    raise SystemExit(1)

def identity(value):
    kind = {
        stat.S_IFDIR: "directory",
        stat.S_IFLNK: "symlink",
        stat.S_IFSOCK: "socket",
    }.get(stat.S_IFMT(value.st_mode), "other")
    return "%x\t%x\t%d\t%s" % (value.st_dev, value.st_ino, value.st_uid, kind)

def read_path(path_file):
    with open(path_file, "rb") as value:
        raw = value.read(4097)
    if not raw or len(raw) > 4096 or b"\0" in raw:
        fail()
    return os.fsdecode(raw)

def run(arguments):
    if not arguments:
        fail()
    if arguments[0] == "stat-path" and len(arguments) == 2:
        print(identity(os.lstat(arguments[1])))
        return
    if arguments[0] == "stat-entry" and len(arguments) == 2:
        print(identity(os.lstat(read_path(arguments[1]))))
        return
    if arguments[0] != "inventory" or len(arguments) != 5:
        fail()
    directory, path_directory = arguments[1], arguments[2]
    start, limit = int(arguments[3]), int(arguments[4])
    if start < 0 or limit < 1 or limit > 4097:
        fail()
    output = []
    with os.scandir(directory) as entries:
        for offset in range(limit):
            try:
                entry = next(entries)
            except StopIteration:
                break
            index = start + offset + 1
            path = os.fsencode(entry.path)
            if not path or len(path) > 4096 or b"\0" in path:
                fail()
            path_file = os.path.join(path_directory, str(index))
            with open(path_file, "xb") as value:
                value.write(path)
            safe = int(not any(byte in path for byte in b"\t\r\n"))
            output.append("%d\t%s\t%d" % (
                index,
                identity(entry.stat(follow_symlinks=False)),
                safe,
            ))
    if output:
        print("\n".join(output))

run(sys.argv[1:])
PY
socket_count=0
inventory_count=0
pane_total=0
pane_rows=0
tmux_base=${TMUX_TMPDIR:-/tmp}
case "$tmux_base" in /*) ;; *) tmux_base=/tmp ;; esac
run_socket_helper() {
	command -v python3 >/dev/null 2>&1 || return 127
	python3 "$work/socket-inventory.py" "$@"
}
[ -x /usr/bin/id ] || {
	echo "ars: cannot inspect external tmux socket directory" >&2
	exit 1
}
socket_helper_uid=$(/usr/bin/id -u)
valid_identity() {
	awk -F '\t' '
		NR != 1 || NF != 4 { exit 1 }
		$1 !~ /^[0-9a-f]+$/ || $2 !~ /^[0-9a-f]+$/ || $3 !~ /^[0-9]+$/ { exit 1 }
		$4 != "directory" && $4 != "symlink" && $4 != "socket" && $4 != "other" { exit 1 }
		END { if (NR != 1) exit 1 }
	' "$1"
}
read_directory_identity() {
	directory=$1
	if ! run_socket_helper stat-path "$directory" >"$work/socket-directory-current" ||
		! valid_identity "$work/socket-directory-current"; then
		echo "ars: cannot inspect external tmux socket directory" >&2
		return 2
	fi
	socket_directory_identity=$(cat "$work/socket-directory-current")
}
validate_socket_dir() {
	directory=$1
	read_directory_identity "$directory" || return 2
	tab=$(printf '\t')
	IFS="$tab" read -r directory_dev directory_inode directory_uid directory_kind <"$work/socket-directory-current"
	if [ "$directory_kind" != directory ] || [ "$directory_uid" != "$socket_helper_uid" ]; then
		echo "ars: invalid external tmux socket directory" >&2
		return 2
	fi
}
inventory_socket_dir() {
	directory=$1
	validate_socket_dir "$directory" || return 2
	socket_inventory_identity=$socket_directory_identity
	remaining=$((4097 - inventory_count))
	if ! run_socket_helper inventory "$directory" "$work/socket-paths" "$inventory_count" "$remaining" >"$work/socket-inventory-current"; then
		echo "ars: cannot inspect external tmux socket directory" >&2
		return 2
	fi
	if ! awk -F '\t' -v start="$inventory_count" '
		NF != 6 || $1 != start + NR || $2 !~ /^[0-9a-f]+$/ || $3 !~ /^[0-9a-f]+$/ ||
			$4 !~ /^[0-9]+$/ ||
			($5 != "directory" && $5 != "symlink" && $5 != "socket" && $5 != "other") ||
			($6 != "0" && $6 != "1") { exit 1 }
	' "$work/socket-inventory-current"; then
		echo "ars: invalid external tmux socket inventory" >&2
		return 2
	fi
	current_count=$(awk 'END { print NR + 0 }' "$work/socket-inventory-current")
	inventory_count=$((inventory_count + current_count))
	if [ "$inventory_count" -gt 4096 ]; then
		echo "ars: external tmux socket directory entries exceed limit" >&2
		return 2
	fi
	cat "$work/socket-inventory-current" >>"$work/socket-inventory"
}
revalidate_socket_dir() {
	directory=$1
	expected_identity=$2
	validate_socket_dir "$directory" || return 2
	if [ "$socket_directory_identity" != "$expected_identity" ]; then
		echo "ars: invalid external tmux socket directory" >&2
		return 2
	fi
}
classify_socket_inventory() {
	tab=$(printf '\t')
	while IFS="$tab" read -r index dev inode owner kind safe; do
		if [ "$kind" = symlink ]; then
			echo "ars: invalid external tmux socket" >&2
			return 2
		fi
		if [ "$kind" = socket ]; then
			if [ "$owner" != "$socket_helper_uid" ] || [ "$safe" != 1 ]; then
				echo "ars: invalid external tmux socket" >&2
				return 2
			fi
			socket_count=$((socket_count + 1))
			if [ "$socket_count" -gt 64 ]; then
				echo "ars: external tmux socket count exceeds limit" >&2
				return 2
			fi
			printf '%s\t%s\t%s\t%s\t%s\n' "$index" "$dev" "$inode" "$owner" "$kind" >>"$work/socket-inventory-sockets"
		fi
	done <"$work/socket-inventory"
}
match_socket_inventory() {
	tab=$(printf '\t')
	while IFS="$tab" read -r index dev inode owner kind; do
		if ! run_socket_helper stat-entry "$work/socket-paths/$index" >"$work/socket-current" ||
			! valid_identity "$work/socket-current"; then
			echo "ars: invalid external tmux socket" >&2
			return 2
		fi
		expected=$(printf '%s\t%s\t%s\t%s' "$dev" "$inode" "$owner" "$kind")
		current=$(cat "$work/socket-current")
		if [ "$current" != "$expected" ]; then
			echo "ars: invalid external tmux socket" >&2
			return 2
		fi
		socket=
		IFS= read -r socket <"$work/socket-paths/$index" || [ -n "$socket" ] || {
			echo "ars: invalid external tmux socket path" >&2
			return 2
		}
		if ! match_panes "$socket" "$dev" "$inode" "$owner"; then
			status=$?
			[ "$status" -eq 2 ] && return 2
			echo "ars: cannot inspect external tmux socket" >&2
			return 2
		fi
	done <"$work/socket-inventory-sockets"
}

primary_dir=$tmux_base/tmux-$uid
fallback_dir=/tmp/tmux-$uid
primary_present=0
fallback_present=0
if [ -e "$primary_dir" ] || [ -L "$primary_dir" ]; then
	primary_present=1
	inventory_socket_dir "$primary_dir" || exit 1
	primary_identity=$socket_inventory_identity
fi
if [ "$fallback_dir" != "$primary_dir" ] && { [ -e "$fallback_dir" ] || [ -L "$fallback_dir" ]; }; then
	fallback_present=1
	inventory_socket_dir "$fallback_dir" || exit 1
	fallback_identity=$socket_inventory_identity
fi
classify_socket_inventory || exit 1
if [ "$primary_present" -eq 1 ]; then
	revalidate_socket_dir "$primary_dir" "$primary_identity" || exit 1
fi
if [ "$fallback_present" -eq 1 ]; then
	revalidate_socket_dir "$fallback_dir" "$fallback_identity" || exit 1
fi
match_socket_inventory || exit 1

resolve_matches || exit 1

match_count=$(wc -l <"$work/matches" | tr -d ' ')
case "$match_count" in
0) printf 'none\n' ;;
1)
tab=$(printf '\t')
IFS="$tab" read -r socket pane pane_pid socket_dev socket_inode socket_uid <"$work/matches"
	printf 'match\t%s\t%s\t%s\t%s\t%s\t%s\tsocket\n' \
		"$socket" "$pane" "$pane_pid" "$socket_dev" "$socket_inode" "$socket_uid"
	;;
*) echo "ars: external tmux conflict" >&2; exit 1 ;;
esac`
