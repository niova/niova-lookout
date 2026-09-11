package monitor

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/00pauln00/niova-lookout/pkg/monitor/applications"
	"github.com/00pauln00/niova-lookout/pkg/xlog"
)

const ssdStatsSleepTime = 60 * time.Second

// nvmeIdNsOutput mirrors the fields of interest from `nvme id-ns -o json`.
// "ncap" (Namespace Capacity) and "nuse" (Namespace Utilization) are NVMe
// Identify-Namespace field names, reported in logical blocks.
type nvmeIdNsOutput struct {
	NCap uint64 `json:"ncap"`
	NUse uint64 `json:"nuse"`
}

// fetchNVMeCapUse shells out to nvme-cli to read a device's Identify
// Namespace capacity/utilization. This issues an NVMe admin-passthru
// command, which requires the calling process to have root or
// CAP_SYS_ADMIN on devPath; otherwise nvme-cli returns a permission error.
func fetchNVMeCapUse(devPath string) (ncap uint64, nuse uint64, err error) {
	out, err := exec.Command("nvme", "id-ns", devPath, "-o", "json").Output()
	if err != nil {
		return 0, 0, fmt.Errorf("nvme id-ns %s: %w", devPath, err)
	}

	var parsed nvmeIdNsOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return 0, 0, fmt.Errorf("parse nvme id-ns output for %s: %w",
			devPath, err)
	}

	return parsed.NCap, parsed.NUse, nil
}

// pollSSDStats fetches NVMe capacity/utilization for every currently
// running NISD's backing device and caches the result on its Nisd app
// instance for the next Prometheus scrape.
func (h *LookoutHandler) pollSSDStats() {
	for _, ep := range h.Epc.TakeSnapshot() {
		if ep.State != EPstateRunning {
			continue
		}

		nisd, ok := ep.App.(*applications.Nisd)
		if !ok {
			continue
		}

		devPath := nisd.GetDevPath()
		if devPath == "" {
			continue
		}

		ncap, nuse, err := fetchNVMeCapUse(devPath)
		if err != nil {
			xlog.Warnf("ssd stats poll for %s (%s): %v",
				nisd.GetUUID().String(), devPath, err)
			continue
		}

		nisd.SetSSDStats(ncap, nuse)
	}
}

// monitorSSDStats runs pollSSDStats every 60s until shutdown.
func (h *LookoutHandler) monitorSSDStats() {
	for h.state != SHUTDOWN {
		time.Sleep(ssdStatsSleepTime)

		h.pollSSDStats()
	}
}
