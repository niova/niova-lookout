package monitor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/00pauln00/niova-lookout/pkg/monitor/applications"
	"github.com/00pauln00/niova-lookout/pkg/xlog"
)

const ssdStatsSleepTime = 60 * time.Second

// DeviceSSDStats pairs a device's polled NVMe capacity/utilization with the
// UUIDs of the NISDs currently reporting that device as their backing store.
type DeviceSSDStats struct {
	Stats     applications.SSDStats
	NisdUUIDs []string
	NodeName  string
}

var partSuffixRe = regexp.MustCompile(`^(/dev/(?:nvme\d+n\d+|mmcblk\d+))p\d+$|^(/dev/(?:sd|vd|xvd)[a-z]+)\d+$`)

// baseDevPath maps a partition path (e.g. /dev/nvme2n1p3) to its parent
// whole-disk path (/dev/nvme2n1). It consults sysfs first and falls back to
// name matching. Non-partition paths are returned unchanged.
func baseDevPath(devPath string) string {
	name := filepath.Base(devPath)
	sysPath := filepath.Join("/sys/class/block", name)
	if _, err := os.Stat(filepath.Join(sysPath, "partition")); err == nil {
		if real, err := filepath.EvalSymlinks(sysPath); err == nil {
			return "/dev/" + filepath.Base(filepath.Dir(real))
		}
	}

	if m := partSuffixRe.FindStringSubmatch(devPath); m != nil {
		if m[1] != "" {
			return m[1]
		}
		return m[2]
	}
	return devPath
}

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

// pollSSDStats groups currently running NISDs by their backing device path
// (as reported via each NISD's ctl-interface), then fetches NVMe
// capacity/utilization exactly once per unique device, caching the result
// on the EPContainer for the next Prometheus scrape. This avoids redundant
// nvme-cli invocations when multiple NISDs share the same physical device.
func (h *LookoutHandler) pollSSDStats() {
	devNisds := make(map[string][]string)
	devNodes := make(map[string]string)

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
		devPath = baseDevPath(devPath)

		if devNodes[devPath] == "" && nisd.EPInfo.SysInfo != nil {
			devNodes[devPath] = nisd.EPInfo.SysInfo.UtsNodename
		}
		devNisds[devPath] = append(devNisds[devPath], nisd.GetUUID().String())
	}

	for devPath, nisdUUIDs := range devNisds {
		ncap, nuse, err := fetchNVMeCapUse(devPath)
		if err != nil {
			xlog.Warnf("ssd stats poll for %s: %v", devPath, err)
			continue
		}

		sort.Strings(nisdUUIDs)
		h.Epc.SetDeviceSSDStats(devPath, DeviceSSDStats{
			Stats:     applications.SSDStats{NCap: ncap, NUse: nuse},
			NisdUUIDs: nisdUUIDs,
			NodeName:  devNodes[devPath],
		})
	}
}

// monitorSSDStats runs pollSSDStats every 60s until shutdown.
func (h *LookoutHandler) monitorSSDStats() {
	for h.state != SHUTDOWN {
		time.Sleep(ssdStatsSleepTime)

		h.pollSSDStats()
	}
}
