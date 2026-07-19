package ssh

import (
	"embed"
	"fmt"
)

//go:embed generated/ars-collector-darwin-arm64
//go:embed generated/ars-collector-linux-amd64
//go:embed generated/ars-collector-linux-arm64
var collectorFiles embed.FS

type EmbeddedCollectorAssets struct{}

func (EmbeddedCollectorAssets) ForTarget(goos, goarch string) ([]byte, error) {
	name, err := collectorAssetName(goos, goarch)
	if err != nil {
		return nil, err
	}
	data, err := collectorFiles.ReadFile("generated/" + name)
	if err != nil {
		return nil, fmt.Errorf("collector asset %s/%s is unavailable: %w", goos, goarch, err)
	}
	return data, nil
}

func collectorAssetName(goos, goarch string) (string, error) {
	name, ok := collectorAssetNames[[2]string{goos, goarch}]
	if !ok {
		return "", fmt.Errorf("unsupported collector target %s/%s", goos, goarch)
	}
	return name, nil
}

var collectorAssetNames = map[[2]string]string{
	{"darwin", "arm64"}: "ars-collector-darwin-arm64",
	{"linux", "amd64"}:  "ars-collector-linux-amd64",
	{"linux", "arm64"}:  "ars-collector-linux-arm64",
}
