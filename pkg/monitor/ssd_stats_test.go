package monitor

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFakeNvmeBinary installs a fake `nvme` executable on PATH (via
// t.Setenv) that prints the given stdout content when invoked, so
// fetchNVMeCapUse can be tested deterministically without real NVMe
// hardware or elevated privileges.
func writeFakeNvmeBinary(t *testing.T, stdout string) {
	t.Helper()

	dir := t.TempDir()
	script := filepath.Join(dir, "nvme")
	content := "#!/bin/sh\ncat <<'NVMEOUT'\n" + stdout + "\nNVMEOUT\n"

	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatalf("write fake nvme binary: %v", err)
	}

	// Prepend dir to the real PATH so the fake nvme binary is found first,
	// while /bin/sh and cat (used by the script itself) remain resolvable.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestFetchNVMeCapUseSuccess(t *testing.T) {
	writeFakeNvmeBinary(t, `{"ncap":123456,"nuse":7890}`)

	ncap, nuse, err := fetchNVMeCapUse("/dev/fake0n1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ncap != 123456 || nuse != 7890 {
		t.Fatalf("got ncap=%d nuse=%d, want ncap=123456 nuse=7890",
			ncap, nuse)
	}
}

func TestFetchNVMeCapUseInvalidJSON(t *testing.T) {
	writeFakeNvmeBinary(t, "not json")

	if _, _, err := fetchNVMeCapUse("/dev/fake0n1"); err == nil {
		t.Fatal("expected a parse error, got nil")
	}
}

func TestFetchNVMeCapUseCommandNotFound(t *testing.T) {
	// Empty PATH: the "nvme" binary cannot be found, exec.Command should
	// fail rather than panicking.
	t.Setenv("PATH", t.TempDir())

	if _, _, err := fetchNVMeCapUse("/dev/fake0n1"); err == nil {
		t.Fatal("expected an error when nvme binary is missing, got nil")
	}
}
