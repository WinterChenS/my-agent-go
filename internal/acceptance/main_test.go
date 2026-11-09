package acceptance

import (
	"fmt"
	"os"
	"testing"
)

// TestMain refuses to run the suite when the binary was re-executed as a
// product command. Without this guard a step using "self" would recursively
// start acceptance.test.exe until the machine runs out of processes.
func TestMain(m *testing.M) {
	if os.Getenv(acceptanceStubEnv) != "" {
		fmt.Fprintln(os.Stderr, "acceptance test binary: stub guard hit; refusing to enter the test suite")
		os.Exit(2)
	}
	os.Exit(m.Run())
}
