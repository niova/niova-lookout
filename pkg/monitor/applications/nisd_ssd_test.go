package applications

import "testing"

func TestNisdSSDStats(t *testing.T) {
	n := &Nisd{}

	if got := n.GetDevPath(); got != "" {
		t.Fatalf("GetDevPath() on empty Nisd = %q, want \"\"", got)
	}

	n.EPInfo.NiorqMgr = []NiorqMgr{{DevPath: "/dev/nvme0n1"}}
	if got := n.GetDevPath(); got != "/dev/nvme0n1" {
		t.Fatalf("GetDevPath() = %q, want /dev/nvme0n1", got)
	}
}
