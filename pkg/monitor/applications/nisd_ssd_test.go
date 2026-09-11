package applications

import "testing"

func TestNisdSSDStats(t *testing.T) {
	n := &Nisd{}

	if got := n.GetDevPath(); got != "" {
		t.Fatalf("GetDevPath() on empty Nisd = %q, want \"\"", got)
	}

	if _, ok := n.GetSSDStats(); ok {
		t.Fatal("GetSSDStats() ok = true before any SetSSDStats call")
	}

	n.SetSSDStats(100, 42)

	stats, ok := n.GetSSDStats()
	if !ok {
		t.Fatal("GetSSDStats() ok = false after SetSSDStats")
	}
	if stats.NCap != 100 || stats.NUse != 42 {
		t.Fatalf("GetSSDStats() = %+v, want NCap=100 NUse=42", stats)
	}

	n.EPInfo.NiorqMgr = []NiorqMgr{{DevPath: "/dev/nvme0n1"}}
	if got := n.GetDevPath(); got != "/dev/nvme0n1" {
		t.Fatalf("GetDevPath() = %q, want /dev/nvme0n1", got)
	}
}
