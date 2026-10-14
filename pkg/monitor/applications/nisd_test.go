package applications

import (
	"encoding/json"
	"strings"
	"testing"

	ph "github.com/00pauln00/niova-lookout/pkg/prometheusHandler"
)

const throttleCtlIfOut = `{
  "nisd_root_entry": [{
    "uuid": "11111111-1111-1111-1111-111111111111",
    "reserved-pblk-pool-pct-used": 42,
    "reserved-write-throttle-engaged": true,
    "write-tasks-running": 12,
    "write-tasks-waiting": 3,
    "write-task-pool-total": 384,
    "write-busy-avg-tau-msec": 10000,
    "shallow-merge-min-tasks": 1,
    "shallow-merge-max-tasks": 8
  }],
  "mb_merge_root_entry": [
    {"mode": "shallow", "dynamic-merge-cap": 4, "configured-merge-handles": 8},
    {"mode": "full", "dynamic-merge-cap": 1, "configured-merge-handles": 1}
  ]
}`

func TestNisdWriteThrottleMetrics(t *testing.T) {
	var c CtlIfOut
	if err := json.Unmarshal([]byte(throttleCtlIfOut), &c); err != nil {
		t.Fatal(err)
	}

	out := ph.GenericPromDataParser(c.NISDRootEntry[0], map[string]string{})
	for _, m := range c.MbMerge {
		out += ph.GenericPromDataParser(m, map[string]string{"NAME": m.Mode})
	}

	for _, want := range []string{
		"nisd_reserved_pblk_pool_pct_used{} 42",
		"nisd_reserved_write_throttle_engaged{} 1",
		"nisd_write_tasks_running{} 12",
		"nisd_write_tasks_waiting{} 3",
		"nisd_write_task_pool_total{} 384",
		"nisd_write_busy_avg_tau_msec{} 10000",
		"nisd_shallow_merge_min_tasks{} 1",
		"nisd_shallow_merge_max_tasks{} 8",
		`merge_dynamic_cap{NAME="shallow"} 4`,
		`merge_configured_handles{NAME="shallow"} 8`,
		`merge_dynamic_cap{NAME="full"} 1`,
		`merge_configured_handles{NAME="full"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}

	c.NISDRootEntry[0].ReservedWriteThrottleEngaged = false
	out = ph.GenericPromDataParser(c.NISDRootEntry[0], map[string]string{})
	if !strings.Contains(out, "nisd_reserved_write_throttle_engaged{} 0") {
		t.Error("throttle flag false should export as 0")
	}
}
