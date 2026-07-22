// Package update checks GitHub Releases for a newer ars version and,
// after user confirmation, applies the update for the active install
// channel before re-executing the new binary.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const DefaultReleaseAPI = "https://api.github.com/repos/baleen37/agent-remote-sessions/releases/latest"

func FetchLatest(ctx context.Context, client *http.Client, apiURL string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("build release request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("fetch latest release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch latest release: status %d", response.StatusCode)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&release); err != nil {
		return "", fmt.Errorf("decode latest release: %w", err)
	}
	latest, ok := strings.CutPrefix(release.TagName, "v")
	if !ok {
		return "", fmt.Errorf("unexpected release tag %q", release.TagName)
	}
	return latest, nil
}

func IsNewer(latest, current string) bool {
	latestParts, ok := parseVersion(latest)
	if !ok {
		return false
	}
	currentParts, ok := parseVersion(current)
	if !ok {
		return false
	}
	for index := range latestParts {
		if latestParts[index] != currentParts[index] {
			return latestParts[index] > currentParts[index]
		}
	}
	return false
}

func parseVersion(version string) ([3]int, bool) {
	pieces := strings.Split(version, ".")
	if len(pieces) != 3 {
		return [3]int{}, false
	}
	var parts [3]int
	for index, piece := range pieces {
		value, err := strconv.Atoi(piece)
		if err != nil || value < 0 || piece != strconv.Itoa(value) {
			return [3]int{}, false
		}
		parts[index] = value
	}
	return parts, true
}
