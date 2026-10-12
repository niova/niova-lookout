package monitor

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"

	"github.com/00pauln00/niova-lookout/pkg/xlog"
)

//var HttpPort int

type lookoutState int

const (
	BOOTING lookoutState = iota
	RUNNING
	SHUTDOWN
)

func (s lookoutState) String() string {
	switch s {
	case BOOTING:
		return "booting"
	case RUNNING:
		return "running"
	case SHUTDOWN:
		return "shutdown"
	default:
		break
	}
	return "unknown"
}

type LookoutInotify struct {
	ifd   int
	wd    int32             // watch descriptor for this LookoutHandler
	wmap  map[int32]*NcsiEP // for endpoints
	mutex sync.Mutex
}

type LookoutHandler struct {
	PromPath string
	HttpPort int
	CTLPath  string
	Statb    syscall.Stat_t
	Epc      *EPContainer
	lsofGen  uint64
	state    lookoutState
	inotify  LookoutInotify
}

func (h *LookoutHandler) LookoutWaitUntilState(s lookoutState) {
	for h.state != s {
		xlog.Debugf("waiting for %s", s.String())
		time.Sleep(1 * time.Second)
	}
}

func (h *LookoutHandler) lookoutLsof() error {
	procEntries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}

	h.lsofGen++

	for _, e := range procEntries {
		pid := e.Name()
		if !e.IsDir() || pid[0] < '0' || pid[0] > '9' {
			continue
		}

		fdDir := filepath.Join("/proc", pid, "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue // probably not accessible
		}

		for _, fd := range fds {
			lnk, err := os.Readlink(filepath.Join(fdDir, fd.Name()))

			if err != nil {
				continue
			}

			if !strings.HasPrefix(lnk, h.CTLPath) {
				continue
			}

			dir := filepath.Dir(lnk)   // /tmp/.niova/<uuid>/input
			base := filepath.Base(dir) // <uuid>
			if u, err := uuid.Parse(base); err == nil {
				// Try to find the endpoint at this uuid
				h.Epc.LsofGenAddOrUpdateEp(h, u)

				break // Only need to process the first found
			}
		}
	}

	return nil
}

func (h *LookoutHandler) monitorLsof() {

	sleepTime := 60 * time.Second

	for h.state != SHUTDOWN {
		time.Sleep(sleepTime)

		h.lookoutLsof()
	}
}

func (h *LookoutHandler) monitor() error {
	var err error = nil
	var sleepTime time.Duration

	if h.state != BOOTING {
		panic("Invalid lookoutState")
	}

	xlog.Info("RUNNING")
	h.state = RUNNING

	sleepEnv := os.Getenv("LOOKOUT_SLEEP")
	if sleepEnv != "" {
		sleepTime, err = time.ParseDuration(sleepEnv)
		if err != nil {
			xlog.Warn("LOOKOUT_SLEEP has invalid contents: defaulting to standard value '20s'\n\t\tSee ParseDuration(): (example: <num-secs>s | <num-ms>ms)")
		}
	}

	if sleepTime == 0 {
		sleepTime = 20 * time.Second
	}

	xlog.Info("Lookout monitor sleep time: ", sleepTime)

	for h.state != SHUTDOWN {
		h.Epc.CleanEPs()
		h.Epc.PollEPs()

		time.Sleep(sleepTime)
	}

	return err
}

func (h *LookoutHandler) writePromPath() error {
	f, err := os.OpenFile(h.PromPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0644)

	if err != nil {
		return err
	}
	_, err = f.WriteString(strconv.Itoa(h.HttpPort))
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}

	return nil
}

type inotifyEvent struct {
	Name  string
	Event unix.InotifyEvent
}

func (h *LookoutHandler) epInotifyWatch() {

	buf := make([]byte, 4096)

	for h.state != SHUTDOWN {

		n, err := unix.Read(h.inotify.ifd, buf)
		if err != nil {
			if err == unix.EAGAIN || err == unix.EINTR {
				// no events available, just continue
				continue
			} else {
				xlog.Fatalf("unix.Read(): %v", err)
			}
		}

		var off uint32
		for off <= uint32(n-unix.SizeofInotifyEvent) {
			ev := (*unix.InotifyEvent)(unsafe.Pointer(&buf[off]))
			nameBytes := buf[off+unix.SizeofInotifyEvent : off+unix.SizeofInotifyEvent+ev.Len]
			name := string(bytes.TrimRight(nameBytes, "\x00"))

			off += unix.SizeofInotifyEvent + ev.Len

			ievent := inotifyEvent{
				Name:  name,
				Event: *ev, // copy struct value
			}

			xlog.Infof("ievent=%v", ievent)

			h.processEvent(&ievent)
		}
	}

	buf = nil
}

func (h *LookoutHandler) EpWatchAdd(ep *NcsiEP, events uint32) error {
	if h.inotify.ifd == -1 || h.inotify.wd == -1 {
		xlog.Fatal("Inotify FDs have not been initialized")
	}

	infy := &h.inotify

	if ep == nil {
		return unix.EINVAL
	}

	if ep.wid != -1 {
		return unix.EBUSY
	}

	// Lock map
	infy.mutex.Lock()
	defer infy.mutex.Unlock()

	wid, err :=
		unix.InotifyAddWatch(h.inotify.ifd, ep.Xpath(EP_PATH_OUTPUT),
			events)

	if err != nil {
		return err
	}

	xlog.FatalIF((wid < 0),
		"unix.InotifyAddWatch() succeeded but wid=%d < 0", wid)

	if infy.wmap[int32(wid)] != nil {
		ep.Log(xlog.FATAL, "ep already exists at wid=%d", wid)
	}

	ep.wid = int32(wid)
	infy.wmap[int32(wid)] = ep

	ep.Log(xlog.INFO, "ok")

	return nil
}

func (h *LookoutHandler) EpWatchRemove(ep *NcsiEP) {
	if h.inotify.ifd == -1 || h.inotify.wd == -1 || ep == nil {
		xlog.Fatal("Inotify FDs have not been initialized")
	}

	if ep.wid < 0 {
		ep.Log(xlog.FATAL, "ep is not being watched")
	}

	infy := &h.inotify

	// Lock map
	infy.mutex.Lock()
	defer infy.mutex.Unlock()

	xep, ok := infy.wmap[ep.wid]
	if !ok {
		ep.Log(xlog.FATAL, "ep does not exist in map")
	}

	if ep != xep {
		xep.Log(xlog.ERROR, "this ep was found when..")
		ep.Log(xlog.FATAL, ".. this one was expected")
	}

	delete(infy.wmap, ep.wid)

	ep.wid = -1

	unix.InotifyRmWatch(h.inotify.ifd, uint32(ep.wid))
}

func (h *LookoutHandler) epWatchLookup(event *inotifyEvent) *NcsiEP {
	infy := &h.inotify

	infy.mutex.Lock()
	defer infy.mutex.Unlock()

	ep, ok := infy.wmap[event.Event.Wd]

	if !ok {
		return nil
	}

	ep.Log(xlog.INFO, "")

	return ep
}

func (h *LookoutHandler) epInotifyStart() {
	h.inotify.ifd = -1
	h.inotify.wd = -1

	var err error

	h.inotify.wmap = make(map[int32](*NcsiEP))

	h.inotify.ifd, err = unix.InotifyInit()

	xlog.FatalIF((err != nil || h.inotify.ifd < 0),
		"unix.InotifyInit(): %v (ifd=%d)", err, h.inotify.ifd)

	var wid int

	// Add watch only for CREATE events
	wid, err = unix.InotifyAddWatch(h.inotify.ifd, h.CTLPath,
		unix.IN_CREATE|unix.IN_ATTRIB)

	xlog.FatalIF((err != nil || wid < 0), "unix.InotifyAddWatch(): %v", err)

	h.inotify.wd = int32(wid)

	go h.epInotifyWatch()
}

func (h *LookoutHandler) processEvent(e *inotifyEvent) {

	xlog.Infof("%v", e)

	if e.Event.Wd == h.inotify.wd {
		epUuid, err := uuid.Parse(e.Name)
		if err != nil {
			xlog.Error("cmd uuid.Parse(): ", err)
			return
		}

		xlog.Infof("h.Epc.AddEp(): ep-uuid: %s", epUuid.String())

		h.Epc.AddEp(h, epUuid)

	} else {
		evfile := e.Name

		ep := h.epWatchLookup(e)

		if ep == nil {
			xlog.Warnf("Failed to find ep@wid=%d", e.Event.Wd)
			return
		}

		//temp file exclusion
		if strings.HasPrefix(evfile, ".") {
			xlog.Debugf("skipping ctl-interface temp file: %s",
				evfile)
			return
		}

		//Only include files contain "lookout"
		if !strings.HasPrefix(evfile, LookoutPrefixStr) {
			xlog.Infof(
				"event %s does not contain prefix string (%s)",
				evfile, LookoutPrefixStr)
			return
		}

		cmdUuid, xerr := uuid.Parse(evfile[len(LookoutPrefixStr):])
		if xerr != nil {
			xlog.Error("cmd uuid.Parse(): ", xerr)
			return
		}

		ep.Log(xlog.INFO, "ev-complete: cmd-uuid=%s", cmdUuid.String())
		ep.Complete(cmdUuid, nil)
	}
}

func (h *LookoutHandler) Start() error {

	var err error

	// Check the provided endpoint root path
	if err = syscall.Stat(h.CTLPath, &h.Statb); err != nil {
		xlog.Error("syscall.Stat(%s): ", h.CTLPath, err)
		return err
	}

	if err = h.Epc.InitializeEpMap(); err != nil {
		xlog.Error("h.Epc.InitializeEpMap(): ", err)
		return err
	}

	h.epInotifyStart()

	h.Epc.HttpQuery = make(map[string](chan []byte))

	if err = h.writePromPath(); err != nil {
		xlog.Error("h.writePromPath(): ", err)
		return err
	}

	// Run the lsof scan to determine alive niova processes
	h.lookoutLsof()

	go h.monitorLsof()

	go h.monitorSSDStats()

	// Start monitoring
	if err = h.monitor(); err != nil {
		xlog.Error("h.monitor(): ", err)
		return err
	}

	return nil
}
