package monitor

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/00pauln00/niova-lookout/pkg/monitor/applications"
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

// writeCountingNvmeBinary installs a fake `nvme` executable on PATH that
// appends a line to countFile on each invocation (so callers can assert how
// many times it ran) before printing stdout, as writeFakeNvmeBinary does.
func writeCountingNvmeBinary(t *testing.T, countFile, stdout string) {
	t.Helper()

	dir := t.TempDir()
	script := filepath.Join(dir, "nvme")
	content := "#!/bin/sh\necho x >> " + countFile + "\ncat <<'NVMEOUT'\n" +
		stdout + "\nNVMEOUT\n"

	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatalf("write fake nvme binary: %v", err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func newRunningNisdEp(devPath string) *NcsiEP {
	nisd := &applications.Nisd{}
	nisd.SetUUID(uuid.New())
	nisd.SetCtlIfOut(applications.CtlIfOut{
		NiorqMgr: []applications.NiorqMgr{{DevPath: devPath}},
	})

	return &NcsiEP{
		Uuid:  nisd.GetUUID(),
		State: EPstateRunning,
		App:   nisd,
		wid:   -1,
	}
}

// TestPollSSDStatsDedupesSharedDevice verifies that when multiple running
// NISDs report the same backing device path, pollSSDStats queries that
// device exactly once and caches a single entry tagged with every NISD UUID
// that shares it.
func TestPollSSDStatsDedupesSharedDevice(t *testing.T) {
	countFile := filepath.Join(t.TempDir(), "count")
	if err := os.WriteFile(countFile, nil, 0644); err != nil {
		t.Fatalf("seed count file: %v", err)
	}
	writeCountingNvmeBinary(t, countFile, `{"ncap":100,"nuse":42}`)

	const devPath = "/dev/nvme0n1"
	ep1 := newRunningNisdEp(devPath)
	ep2 := newRunningNisdEp(devPath)

	epc := &EPContainer{
		epMap: map[uuid.UUID]*NcsiEP{
			ep1.Uuid: ep1,
			ep2.Uuid: ep2,
		},
	}
	h := &LookoutHandler{Epc: epc}

	h.pollSSDStats()

	countData, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatalf("read count file: %v", err)
	}
	if got, want := strings.Count(string(countData), "x\n"), 1; got != want {
		t.Fatalf("nvme invoked %d times, want %d", got, want)
	}

	snap := epc.DeviceSSDStatsSnapshot()
	if len(snap) != 1 {
		t.Fatalf("DeviceSSDStatsSnapshot() has %d entries, want 1: %+v",
			len(snap), snap)
	}

	entry, ok := snap[devPath]
	if !ok {
		t.Fatalf("DeviceSSDStatsSnapshot() missing entry for %s: %+v",
			devPath, snap)
	}
	if entry.Stats.NCap != 100 || entry.Stats.NUse != 42 {
		t.Fatalf("entry.Stats = %+v, want NCap=100 NUse=42", entry.Stats)
	}

	wantUUIDs := []string{ep1.Uuid.String(), ep2.Uuid.String()}
	if wantUUIDs[0] > wantUUIDs[1] {
		wantUUIDs[0], wantUUIDs[1] = wantUUIDs[1], wantUUIDs[0]
	}
	if !reflect.DeepEqual(entry.NisdUUIDs, wantUUIDs) {
		t.Fatalf("entry.NisdUUIDs = %v, want %v (sorted)",
			entry.NisdUUIDs, wantUUIDs)
	}
}

func TestEPContainerDeviceSSDStatsSnapshotIsIndependent(t *testing.T) {
	epc := &EPContainer{}

	epc.SetDeviceSSDStats("/dev/nvme0n1", DeviceSSDStats{
		Stats:     applications.SSDStats{NCap: 1, NUse: 2},
		NisdUUIDs: []string{"a"},
	})

	snap := epc.DeviceSSDStatsSnapshot()
	snap["/dev/nvme0n1"] = DeviceSSDStats{Stats: applications.SSDStats{NCap: 999}}

	again := epc.DeviceSSDStatsSnapshot()
	if again["/dev/nvme0n1"].Stats.NCap != 1 {
		t.Fatalf("mutating a snapshot leaked into the cache: got %+v",
			again["/dev/nvme0n1"])
	}
}
