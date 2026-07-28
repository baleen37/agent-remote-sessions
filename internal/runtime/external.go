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
	if value[index] == '0' {
		return false
	}
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
	rm -f -- "$work/processes" "$work/candidates" "$work/valid-candidates" "$work/panes" "$work/matches"
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
if ! awk '
{
	if (NF != 3 || $1 !~ /^[1-9][0-9]*$/ || $2 !~ /^[0-9]+$/ || $3 !~ /^[^[:space:]]+$/) {
		invalid = 1
		next
	}
	if (seen[$1]++) invalid = 1
}
END { if (NR > 65536 || invalid) exit 1 }
' "$work/processes"; then
	echo "ars: invalid external process table" >&2
	exit 1
fi

awk -v provider="$provider" '$3 == provider { print $1 }' "$work/processes" >"$work/candidates"
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

is_ancestor() {
	pane_pid=$1
	current=$2
	depth=0
	seen=" $current "
	while :; do
		[ "$current" = "$pane_pid" ] && return 0
		parent=$(awk -v pid="$current" '$1 == pid { print $2; exit }' "$work/processes")
		[ -n "$parent" ] || return 1
		depth=$((depth + 1))
		if [ "$depth" -gt 256 ]; then
			echo "ars: external process ancestry exceeds limit" >&2
			return 2
		fi
		case "$seen" in
		*" $parent "*)
			echo "ars: external process ancestry cycle" >&2
			return 2
			;;
		esac
		seen="$seen$parent "
		current=$parent
	done
}

match_panes() {
	socket=$1
	if ! tmux -S "$socket" -f /dev/null list-panes -a -F '#{session_id}:#{window_index}.#{pane_index}\t#{pane_pid}' >"$work/panes"; then
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
	if (NF != 2 || $1 !~ /^\$[1-9][0-9]*:[0-9]+\.[0-9]+$/ || $2 !~ /^[1-9][0-9]*$/) {
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
	tab=$(printf '\t')
	while IFS="$tab" read -r pane pane_pid; do
		while IFS= read -r candidate; do
			if is_ancestor "$pane_pid" "$candidate"; then
				printf '%s\t%s\n' "$socket" "$pane" >>"$work/matches"
				break
			else
				status=$?
				if [ "$status" -eq 2 ]; then
					return 2
				fi
			fi
		done <"$work/valid-candidates"
	done <"$work/panes"
}

: >"$work/matches"
socket_count=0
pane_total=0
tmux_base=${TMUX_TMPDIR:-/tmp}
case "$tmux_base" in /*) ;; *) tmux_base=/tmp ;; esac
inspect_socket_dir() {
	directory=$1
	[ -d "$directory" ] || return 0
	for socket in "$directory"/*; do
		[ -S "$socket" ] || continue
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
