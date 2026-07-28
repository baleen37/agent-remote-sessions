package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

type ExternalTarget struct {
	Socket string
	Pane   string
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
	if len(fields) != 3 || fields[0] != "match" {
		return ExternalTarget{}, false, errors.New("invalid external tmux result")
	}
	target := ExternalTarget{Socket: fields[1], Pane: fields[2]}
	if !filepath.IsAbs(target.Socket) || strings.ContainsAny(target.Socket, "\t\r\n\x00") || !validExternalPane(target.Pane) {
		return ExternalTarget{}, false, errors.New("invalid external tmux target")
	}
	return target, true, nil
}

func validExternalPane(value string) bool {
	if len(value) < 6 || value[0] != '$' {
		return false
	}
	index := 1
	for index < len(value) && value[index] >= '0' && value[index] <= '9' {
		index++
	}
	if index == 1 || index == len(value) || value[index] != ':' {
		return false
	}
	index++
	windowStart := index
	for index < len(value) && value[index] >= '0' && value[index] <= '9' {
		index++
	}
	if index == windowStart || index == len(value) || value[index] != '.' {
		return false
	}
	index++
	paneStart := index
	for index < len(value) && value[index] >= '0' && value[index] <= '9' {
		index++
	}
	return index == len(value) && index > paneStart
}

func ExternalResolverScript() string {
	return externalResolverScript
}

const externalResolverScript = `set -eu
LC_ALL=C
export LC_ALL
provider=$1
native_id=$2
uid=$(id -u)
tmp_base=${TMPDIR:-/tmp}
case "$tmp_base" in /*) ;; *) tmp_base=/tmp ;; esac
work=$(mktemp -d "$tmp_base/ars-external.XXXXXX")
argv_helper=
argv_watch=
inventory_find=
cleanup() {
	if [ -n "$inventory_find" ]; then
		kill "$inventory_find" 2>/dev/null || :
		wait "$inventory_find" 2>/dev/null || :
	fi
	if [ -n "$argv_watch" ]; then
		kill "$argv_watch" 2>/dev/null || :
		wait "$argv_watch" 2>/dev/null || :
	fi
	if [ -n "$argv_helper" ]; then
		kill -KILL "$argv_helper" 2>/dev/null || :
		wait "$argv_helper" 2>/dev/null || :
	fi
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
	/usr/bin/osascript -l JavaScript "$work/argv.js" "$provider" "$resume_arg" "$native_id" "$work/candidates" >"$work/valid-candidates" 2>"$work/argv-error" &
	argv_helper=$!
	(
		sleep 30
		kill "$argv_helper" 2>/dev/null || :
		sleep 1
		kill -KILL "$argv_helper" 2>/dev/null || :
	) </dev/null >/dev/null 2>&1 &
	argv_watch=$!
	if wait "$argv_helper"; then
		argv_status=0
	else
		argv_status=$?
	fi
	argv_helper=
	kill "$argv_watch" 2>/dev/null || :
	wait "$argv_watch" 2>/dev/null || :
	argv_watch=
	if [ "$argv_status" -ne 0 ]; then
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
	if ! tmux -S "$socket" -f /dev/null list-panes -a -F '#{session_id}:#{window_index}.#{pane_index}|#{pane_pid}' >"$work/panes"; then
		echo "ars: cannot inspect external tmux socket" >&2
		return 2
	fi
	pane_bytes=$(wc -c <"$work/panes" | tr -d ' ')
	pane_total=$((pane_total + pane_bytes))
	if [ "$pane_total" -gt 2097152 ]; then
		echo "ars: external tmux pane table exceeds limit" >&2
		return 2
	fi
	if ! awk -F '|' '
{
	if (NF != 2 || $1 !~ /^\$[0-9]+:[0-9]+\.[0-9]+$/ || $2 !~ /^[1-9][0-9]*$/) {
		invalid = 1
		next
	}
	if (seen[$1]++) invalid = 1
}
END { if (NR > 16384 || invalid) exit 1 }
' "$work/panes"; then
		echo "ars: invalid external tmux pane table" >&2
		return 2
	fi
	pane_rows=$((pane_rows + $(awk 'END { print NR }' "$work/panes")))
	if [ "$pane_rows" -gt 16384 ]; then
		echo "ars: external tmux pane row count exceeds limit" >&2
		return 2
	fi
	awk -F '|' -v socket="$socket" '{ print socket "\t" $1 "\t" $2 }' "$work/panes" >>"$work/all-panes"
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
	targets[$3] = targets[$3] $1 "\t" $2 "\n"
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
mkdir "$work/socket-inventory-matches"
: >"$work/socket-inventory"
: >"$work/socket-inventory-status"
printf '0\n' >"$work/socket-inventory-count"
cat >"$work/socket-inventory-helper" <<'EOF'
set -eu
inventory_file=$1
count_file=$2
status_file=$3
producer_file=$4
shift 4
stop_inventory() {
	trap - 0 HUP INT TERM
	printf '%s\n' "$1" >"$status_file"
	while [ ! -s "$producer_file" ]; do :; done
	kill -TERM "$(cat "$producer_file")" 2>/dev/null || :
	exit 2
}
trap 'stop_inventory helper' 0 HUP INT TERM
count=$(cat "$count_file")
newline='
'
for entry do
	if [ "$count" -ge 4096 ]; then
		stop_inventory entries
	fi
	case "$entry" in
	*"$newline"*)
		stop_inventory path
		;;
	esac
	count=$((count + 1))
	printf '%s\n' "$entry"
done >>"$inventory_file"
printf '%s\n' "$count" >"$count_file"
trap - 0 HUP INT TERM
EOF
socket_count=0
pane_total=0
pane_rows=0
tmux_base=${TMUX_TMPDIR:-/tmp}
case "$tmux_base" in /*) ;; *) tmux_base=/tmp ;; esac
owned_path() {
	owner=$(ls -ldn "$1" | awk 'NF >= 3 { print $3; exit }')
	[ "$owner" = "$uid" ]
}
socket_dir_inode() {
	ls -idn "$1" | awk 'NF >= 1 { print $1; exit }'
}
validate_socket_dir() {
	directory=$1
	if [ -L "$directory" ] || [ ! -d "$directory" ] || ! owned_path "$directory"; then
		echo "ars: invalid external tmux socket directory" >&2
		return 2
	fi
	if [ ! -r "$directory" ] || [ ! -x "$directory" ]; then
		echo "ars: cannot inspect external tmux socket directory" >&2
		return 2
	fi
}
inventory_socket_dir() {
	directory=$1
	validate_socket_dir "$directory" || return 2
	socket_inventory_inode=$(socket_dir_inode "$directory")
	if [ -z "$socket_inventory_inode" ]; then
		echo "ars: cannot inspect external tmux socket directory" >&2
		return 2
	fi
	: >"$work/socket-inventory-status"
	: >"$work/socket-inventory-producer"
	find "$directory" -mindepth 1 -maxdepth 1 -exec /bin/sh "$work/socket-inventory-helper" "$work/socket-inventory" "$work/socket-inventory-count" "$work/socket-inventory-status" "$work/socket-inventory-producer" '{}' + &
	inventory_find=$!
	printf '%s\n' "$inventory_find" >"$work/socket-inventory-producer"
	if wait "$inventory_find"; then
		inventory_result=0
	else
		inventory_result=$?
	fi
	inventory_find=
	if [ "$inventory_result" -ne 0 ]; then
		reason=$(cat "$work/socket-inventory-status")
		case "$reason" in
		entries) echo "ars: external tmux socket directory entries exceed limit" >&2 ;;
		path) echo "ars: invalid external tmux socket path" >&2 ;;
		*) echo "ars: cannot inspect external tmux socket directory" >&2 ;;
		esac
		return 2
	fi
}
revalidate_socket_dir() {
	directory=$1
	expected_inode=$2
	validate_socket_dir "$directory" || return 2
	if [ "$(socket_dir_inode "$directory")" != "$expected_inode" ]; then
		echo "ars: invalid external tmux socket directory" >&2
		return 2
	fi
}
classify_socket_inventory() {
	index=0
	while IFS= read -r socket; do
		index=$((index + 1))
		if [ ! -e "$socket" ] && [ ! -L "$socket" ]; then
			echo "ars: invalid external tmux socket" >&2
			return 2
		fi
		if [ -L "$socket" ]; then
			echo "ars: invalid external tmux socket" >&2
			return 2
		fi
		if [ -S "$socket" ]; then
			socket_count=$((socket_count + 1))
			if [ "$socket_count" -gt 64 ]; then
				echo "ars: external tmux socket count exceeds limit" >&2
				return 2
			fi
			: >"$work/socket-inventory-matches/$index"
		fi
	done <"$work/socket-inventory"
}
match_socket_inventory() {
	index=0
	while IFS= read -r socket; do
		index=$((index + 1))
		if [ ! -e "$socket" ] || [ -L "$socket" ]; then
			echo "ars: invalid external tmux socket" >&2
			return 2
		fi
		if [ ! -e "$work/socket-inventory-matches/$index" ]; then
			if [ -S "$socket" ]; then
				echo "ars: invalid external tmux socket" >&2
				return 2
			fi
			continue
		fi
		if [ ! -S "$socket" ]; then
			echo "ars: invalid external tmux socket" >&2
			return 2
		fi
		if ! owned_path "$socket"; then
			echo "ars: invalid external tmux socket" >&2
			return 2
		fi
		if ! match_panes "$socket"; then
			status=$?
			[ "$status" -eq 2 ] && return 2
			echo "ars: cannot inspect external tmux socket" >&2
			return 2
		fi
	done <"$work/socket-inventory"
}

primary_dir=$tmux_base/tmux-$uid
fallback_dir=/tmp/tmux-$uid
primary_present=0
fallback_present=0
if [ -e "$primary_dir" ] || [ -L "$primary_dir" ]; then
	primary_present=1
	inventory_socket_dir "$primary_dir" || exit 1
	primary_inode=$socket_inventory_inode
fi
if [ "$fallback_dir" != "$primary_dir" ] && { [ -e "$fallback_dir" ] || [ -L "$fallback_dir" ]; }; then
	fallback_present=1
	inventory_socket_dir "$fallback_dir" || exit 1
	fallback_inode=$socket_inventory_inode
fi
classify_socket_inventory || exit 1
if [ "$primary_present" -eq 1 ]; then
	revalidate_socket_dir "$primary_dir" "$primary_inode" || exit 1
fi
if [ "$fallback_present" -eq 1 ]; then
	revalidate_socket_dir "$fallback_dir" "$fallback_inode" || exit 1
fi
match_socket_inventory || exit 1

resolve_matches || exit 1

match_count=$(wc -l <"$work/matches" | tr -d ' ')
case "$match_count" in
0) printf 'none\n' ;;
1)
tab=$(printf '\t')
IFS="$tab" read -r socket pane <"$work/matches"
	printf 'match\t%s\t%s\n' "$socket" "$pane"
	;;
*) echo "ars: external tmux conflict" >&2; exit 1 ;;
esac`
