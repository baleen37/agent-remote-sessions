package update

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

const npmPackagePath = "node_modules/@baleen37/ars/"

// IsNPMInstall reports whether the running executable is the vendored
// binary inside the @baleen37/ars npm package.
func IsNPMInstall(executable string) bool {
	return strings.Contains(filepath.ToSlash(executable), npmPackagePath)
}

type CommandRunner func(ctx context.Context, name string, args ...string) error

func ApplyNPM(ctx context.Context, run CommandRunner, version string) error {
	if err := run(ctx, "npm", "install", "-g", "@baleen37/ars@"+version); err != nil {
		return fmt.Errorf("npm install: %w", err)
	}
	return nil
}
