package monitor

import (
	"encoding/json"
	"sync"
	"syscall"

	"github.com/google/uuid"

	"github.com/00pauln00/niova-lookout/pkg/monitor/applications"
	"github.com/00pauln00/niova-lookout/pkg/xlog"
)

type EPContainer struct {
	MonitorUUID      string
	SerfMembershipCB func() map[string]bool
	epMap            map[uuid.UUID]*NcsiEP
	mutex            sync.Mutex
	HttpQuery        map[string](chan []byte)
	ssdStatsMu       sync.RWMutex
	deviceSSDStats   map[string]DeviceSSDStats
}

// SetDeviceSSDStats records the latest polled NVMe capacity/utilization for
// a unique backing device, along with the NISD UUIDs currently reporting it
// as their backing store. Safe for concurrent use with
// DeviceSSDStatsSnapshot.
func (epc *EPContainer) SetDeviceSSDStats(devPath string, entry DeviceSSDStats) {
	epc.ssdStatsMu.Lock()
	defer epc.ssdStatsMu.Unlock()
	if epc.deviceSSDStats == nil {
		epc.deviceSSDStats = make(map[string]DeviceSSDStats)
	}
	epc.deviceSSDStats[devPath] = entry
}

// DeviceSSDStatsSnapshot returns a copy of the current per-device NVMe
// capacity/utilization cache, keyed by device path.
func (epc *EPContainer) DeviceSSDStatsSnapshot() map[string]DeviceSSDStats {
	epc.ssdStatsMu.RLock()
	defer epc.ssdStatsMu.RUnlock()
	snap := make(map[string]DeviceSSDStats, len(epc.deviceSSDStats))
	for k, v := range epc.deviceSSDStats {
		snap[k] = v
	}
	return snap
}

//XXX this looks sketchy
func (epc *EPContainer) GetList() map[uuid.UUID]*NcsiEP {
	epc.mutex.Lock()
	defer epc.mutex.Unlock()
	return epc.epMap
}

func (epc *EPContainer) MarkAlive(serviceUUID string) error {
	serviceID, err := uuid.Parse(serviceUUID)
	if err != nil {
		return err
	}
	svc, ok := epc.epMap[serviceID]
	if ok && (svc.State == EPstateInit || svc.State == EPstateDown) {
		panic("This code path is broken")
		svc.pendingCmds = make(map[uuid.UUID]*epCommand)

		svc.ChangeState(EPstateRunning)
	}
	return nil
}

func (epc *EPContainer) CleanEPs() {
	epc.mutex.Lock()
	defer epc.mutex.Unlock()

	for _, ep := range epc.epMap {

		// Flush all expired cmds
		ep.flushStaleCmds()
		ep.RemoveStaleFiles()

		if ep.State == EPstateRunning && ep.LastSeenIsStale() {
			ep.ChangeState(EPstateDown)

		} else if ep.LsofGenIsStale() {
			// Items that eligible for Removing state
			if ep.State == EPstateRunning {
				ep.Log(xlog.WARN,
					"running ep has expired lsof gen")
			} else {
				ep.ChangeState(EPstateRemoving)
			}
		}
	}
}

// At some point we may want to issue the cmds to a priority heap so their
// submission can be spread out over time
func (epc *EPContainer) PollEPs() {
	epc.mutex.Lock()
	defer epc.mutex.Unlock()

	for _, ep := range epc.epMap {
		err := ep.Poll()
		if err != nil {
			xlog.Error("ep.Poll(): ", err)
		}
	}
}

// func (epc *EPContainer) HandleHttpQuery(cmpstr string, uuid uuid.UUID) {
// 	if strings.Contains(cmpstr, "HTTP") {
// 		var output []byte
// 		if ep := epc.epMap[uuid]; ep != nil {
// 			err := ep.Complete(cmpstr, &output)
// 			if err != nil {
// 				output = []byte(err.Error())
// 			}
// 		}

// 		if channel, ok := epc.HttpQuery[cmpstr]; ok {
// 			channel <- output
// 		}
// 		return
// 	}
// }

func (epc *EPContainer) Process(epUuid uuid.UUID, cmdUuid uuid.UUID) {
	if ep := epc.Lookup(epUuid); ep != nil {
		ep.Complete(cmdUuid, nil)
	}
}

func (epc *EPContainer) Lookup(node uuid.UUID) *NcsiEP {
	epc.mutex.Lock()
	defer epc.mutex.Unlock()
	return epc.epMap[node]
}

func (epc *EPContainer) TakeSnapshot() map[uuid.UUID]*NcsiEP {
	nodeMap := make(map[uuid.UUID]*NcsiEP)
	epc.mutex.Lock()
	defer epc.mutex.Unlock()
	for k, v := range epc.epMap {
		nodeMap[k] = v
	}
	return nodeMap
}

func (epc *EPContainer) AddEp(lh *LookoutHandler, epUuid uuid.UUID) bool {
	epc.mutex.Lock()
	defer epc.mutex.Unlock()

	// Update the Gen regardless
	if xep := epc.epMap[epUuid]; xep != nil {
		xep.LsofGenUpdate(lh.lsofGen)

		var newState = xep.State

		// Note that ep's which are down but are still detected by lsof
		// will be held in the down position.  The can be brought back
		// to 'running' via Poll
		switch xep.State {
		case EPstateRemoving:
			newState = EPstateInit
		case EPstateUnknown:
			newState = EPstateInit
		default:
		}

		if newState != xep.State {
			xep.ChangeState(newState)
		}

		return false
	}

	// Create new object
	ep := NcsiEP{
		Uuid:        epUuid,
		lh:          lh,
		State:       EPstateUnknown,
		pendingCmds: make(map[uuid.UUID]*epCommand),
		App:         &applications.Unrecognized{},
		lsofGen:     lh.lsofGen,
		wid:         -1,
	}

	ep.ChangeState(EPstateInit)

	epc.epMap[epUuid] = &ep

	return true
}

func (epc *EPContainer) LsofGenAddOrUpdateEp(lh *LookoutHandler,
	epUuid uuid.UUID) {

	addedHere := epc.AddEp(lh, epUuid)

	xlog.Debugf("%s: added here? %v", epUuid.String(), addedHere)
}

func (epc *EPContainer) JsonMarshal(state Epstate) []byte {
	var jsonData []byte
	var err error

	epc.mutex.Lock()
	defer epc.mutex.Unlock()

	if state == EPstateAny {
		jsonData, err = json.MarshalIndent(epc.epMap, "", "\t")

	} else {
		// Exclude items which are not in the Running state
		filtered := make(map[uuid.UUID]*NcsiEP)
		for k, v := range epc.epMap {
			if v.State == state {
				v.Log(xlog.TRACE, "including in http reply")
				filtered[k] = v
			}
		}
		jsonData, err = json.MarshalIndent(filtered, "", "\t")
		filtered = nil
	}

	if err != nil {
		return nil
	}

	return jsonData
}

func (epc *EPContainer) JsonMarshalUUID(uuid uuid.UUID) []byte {
	var jsonData []byte
	var err error

	ep := epc.Lookup(uuid)

	if ep != nil {
		jsonData, err = json.MarshalIndent(ep, "", "\t")
	} else {
		// Return an empty set if the item does not exist
		jsonData = []byte("{}")
	}

	if err != nil {
		return nil
	}

	return jsonData
}

func (epc *EPContainer) InitializeEpMap() error {
	epc.epMap = make(map[uuid.UUID]*NcsiEP)
	if epc.epMap == nil {
		return syscall.ENOMEM
	}
	return nil
}
