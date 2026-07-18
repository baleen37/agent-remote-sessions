package provider_test

import (
	"os/exec"
	"testing"
)

func TestExternalConsumerCanUseProvider(t *testing.T) {
	command := exec.Command("go", "test", "./testdata/external")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("external provider consumer failed to build: %v\n%s", err, output)
	}
}
