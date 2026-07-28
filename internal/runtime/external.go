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
provider=$1
native_id=$2
uid=$(id -u)
tmp_base=${TMPDIR:-/tmp}
case "$tmp_base" in /*) ;; *) tmp_base=/tmp ;; esac
work=$(mktemp -d "$tmp_base/ars-external.XXXXXX")
cleanup() {
	rm -f -- "$work/processes" "$work/parents" "$work/candidates" "$work/valid-candidates" "$work/panes" "$work/all-panes" "$work/matches" "$work/resolve-status" "$work/resolve-steps"
	rmdir -- "$work"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

ps -U "$uid" -o pid=,ppid=,comm= >"$work/processes"
process_bytes=$(wc -c <"$work/processes" | tr -d ' ')
[ "$process_bytes" -le 2097152 ] || {
	echo "ars: external process table exceeds limit" >&2
	exit 1
}
: >"$work/parents"
: >"$work/candidates"
if ! awk -v provider="$provider" -v parents="$work/parents" -v candidates="$work/candidates" '
{
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
	if (name == provider) print pid > candidates
}
END {
	close(parents)
	close(candidates)
	if (NR > 65536 || invalid) exit 1
}
' "$work/processes"; then
	echo "ars: invalid external process table" >&2
	exit 1
fi

: >"$work/valid-candidates"
while IFS= read -r candidate; do
	args=$(ps -p "$candidate" -o args=)
	case "$provider" in
	claude)
		case "$args" in
			"claude --resume $native_id"|/claude\ --resume\ "$native_id"|/*/claude\ --resume\ "$native_id") printf '%s\n' "$candidate" >>"$work/valid-candidates" ;;
			*) : ;;
		esac
		;;
	codex)
		case "$args" in
			"codex resume $native_id"|/codex\ resume\ "$native_id"|/*/codex\ resume\ "$native_id") printf '%s\n' "$candidate" >>"$work/valid-candidates" ;;
			*) : ;;
		esac
		;;
	*) : ;;
	esac
done <"$work/candidates"

match_panes() {
	socket=$1
	if ! tmux -S "$socket" -f /dev/null list-panes -a -F '#{session_id}:#{window_index}.#{pane_index}	#{pane_pid}' >"$work/panes"; then
		echo "ars: cannot inspect external tmux socket" >&2
		return 2
	fi
	pane_bytes=$(wc -c <"$work/panes" | tr -d ' ')
	pane_total=$((pane_total + pane_bytes))
	if [ "$pane_total" -gt 2097152 ]; then
		echo "ars: external tmux pane table exceeds limit" >&2
		return 2
	fi
	if ! awk -F '\t' '
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
	awk -F '\t' -v socket="$socket" '{ print socket "\t" $1 "\t" $2 }' "$work/panes" >>"$work/all-panes"
}

resolve_matches() {
	: >"$work/resolve-status"
	: >"$work/resolve-steps"
	if ! awk -F '\t' -v parents="$work/parents" -v candidates="$work/valid-candidates" -v status="$work/resolve-status" -v steps_file="$work/resolve-steps" -v max_steps="20000" '
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
socket_count=0
pane_total=0
pane_rows=0
tmux_base=${TMUX_TMPDIR:-/tmp}
case "$tmux_base" in /*) ;; *) tmux_base=/tmp ;; esac
owned_path() {
	owner=$(ls -ldn "$1" | awk 'NF >= 3 { print $3; exit }')
	[ "$owner" = "$uid" ]
}
inspect_socket_dir() {
	directory=$1
	if [ ! -e "$directory" ] && [ ! -L "$directory" ]; then
		return 0
	fi
	if [ -L "$directory" ] || [ ! -d "$directory" ] || ! owned_path "$directory"; then
		echo "ars: invalid external tmux socket directory" >&2
		return 2
	fi
	for socket in "$directory"/*; do
		if [ ! -e "$socket" ] && [ ! -L "$socket" ]; then
			continue
		fi
		if [ -L "$socket" ]; then
			echo "ars: invalid external tmux socket" >&2
			return 2
		fi
		[ -S "$socket" ] || continue
		if ! owned_path "$socket"; then
			echo "ars: invalid external tmux socket" >&2
			return 2
		fi
		socket_count=$((socket_count + 1))
		if [ "$socket_count" -gt 64 ]; then
			echo "ars: external tmux socket count exceeds limit" >&2
			return 2
		fi
		if ! match_panes "$socket"; then
			status=$?
			[ "$status" -eq 2 ] && return 2
			echo "ars: cannot inspect external tmux socket" >&2
			return 2
		fi
	done
}

primary_dir=$tmux_base/tmux-$uid
fallback_dir=/tmp/tmux-$uid
inspect_socket_dir "$primary_dir" || exit 1
if [ "$fallback_dir" != "$primary_dir" ]; then
	inspect_socket_dir "$fallback_dir" || exit 1
fi

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
