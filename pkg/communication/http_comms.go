package communication

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	httpc "github.com/00pauln00/niova-pumicedb/go/pkg/utils/httpclient"
	serf "github.com/00pauln00/niova-pumicedb/go/pkg/utils/serfagent"
	sd "github.com/00pauln00/niova-pumicedb/go/pkg/utils/servicediscovery"

	"github.com/00pauln00/niova-lookout/pkg/monitor"
	ph "github.com/00pauln00/niova-lookout/pkg/prometheusHandler"
	"github.com/00pauln00/niova-lookout/pkg/xlog"
)

type CommHandler struct {
	Addr          net.IP   //string
	addrList      []net.IP //[]string
	RecvdPort     int
	StorageClient sd.ServiceDiscoveryHandler
	UdpSocket     net.PacketConn
	UdpPort       string
	//serf
	SerfHandler       serf.SerfAgentHandler
	AgentName         string
	AgentRPCPort      int16
	GossipNodesPath   string
	SerfLogger        string
	RaftUUID          string
	PortRange         []uint16
	ServicePortRangeS uint16
	ServicePortRangeE uint16
	HttpPort          int
	RetPort           *int
	Epc               *monitor.EPContainer
	LookoutUUID       string
	Lookouts          map[string]*LookoutInfo
	mu                sync.Mutex
}

type LookoutInfo struct {
	IPAddrs   []string
	HTTPPort  string
	PortRange string
	Apps      map[uuid.UUID]MonitoredApp
	LastSeen  string
}
type MonitoredApp struct {
	Status string
	Type   string
}

type UdpMessage struct {
	addr    net.Addr
	message []byte
}

func (h *CommHandler) CheckHTTPLiveness() {
	var emptyByteArray []byte
	for {
		url := "127.0.0.1:" + strconv.Itoa(h.HttpPort) + "/check"
		xlog.Infof("url=%s", url)

		_, err := httpc.HTTP_Request(emptyByteArray, url, false)
		if err != nil {
			xlog.Error("HTTP Liveness - ", err)
		} else {
			xlog.Debug("HTTP Liveness - HTTP Server is alive")
			break
		}
		time.Sleep(1 * time.Second)
	}
}

func (h *CommHandler) httpHandleRootRequest(w http.ResponseWriter,
	state monitor.Epstate) {
	fmt.Fprintf(w, "%s\n", string(h.Epc.JsonMarshal(state)))
}

func (h *CommHandler) httpHandleUUIDRequest(w http.ResponseWriter,
	uuid uuid.UUID) {
	fmt.Fprintf(w, "%s\n", string(h.Epc.JsonMarshalUUID(uuid)))
}

func (h *CommHandler) httpHandleRoute(w http.ResponseWriter, r *url.URL) {
	splitURL := strings.Split(r.String(), "/v0/")

	if len(splitURL) == 2 && len(splitURL[1]) == 0 {
		h.httpHandleRootRequest(w, monitor.EPstateRunning)

	} else if splitURL[1] == "all" {
		h.httpHandleRootRequest(w, monitor.EPstateAny)

	} else if splitURL[1] == "down" {
		h.httpHandleRootRequest(w, monitor.EPstateDown)

	} else if splitURL[1] == "init" {
		h.httpHandleRootRequest(w, monitor.EPstateInit)

	} else if splitURL[1] == "removing" {
		h.httpHandleRootRequest(w, monitor.EPstateRemoving)

	} else if uuid, err := uuid.Parse(splitURL[1]); err == nil {
		h.httpHandleUUIDRequest(w, uuid)

	} else {
		fmt.Fprintln(w, "Invalid request: url", splitURL[1])
	}
}

func (h *CommHandler) HttpHandle(w http.ResponseWriter, r *http.Request) {
	h.httpHandleRoute(w, r.URL)
}

func (h *CommHandler) ServeHttp() error {
	mux := http.NewServeMux()
	//	mux.HandleFunc("/v1/", h.QueryHandle)

	//TODO: fix some value not being populated?
	mux.HandleFunc("/v0/", h.HttpHandle)

	//TODO: fix lookouts to report status of other lookouts.
	mux.HandleFunc("/lookouts", h.LookoutsHandler)
	mux.HandleFunc("/metrics", h.MetricsHandler)

	for i := len(h.PortRange) - 1; i >= 0; i-- {
		l, err := net.Listen("tcp", ":"+strconv.Itoa(h.HttpPort))
		if err != nil {
			if strings.Contains(err.Error(), "bind") {
				continue
			} else {
				xlog.Error("net.Listen(): ", err)
				return err
			}
		} else {
			go func() {
				// Reenable to wait for lookout startup
				//monitor.LookoutWaitUntilReady()

				*h.RetPort = h.HttpPort
				xlog.Info("Serving at: ", h.HttpPort)
				http.Serve(l, mux)
			}()
		}
		break
	}
	return nil
}

// func (h *CommHandler) customQuery(node uuid.UUID, query string) []byte {
// 	ep := h.Epc.Lookup(node)
// 	//If not present
// 	if ep == nil {
// 		return []byte("Specified App is not present")
// 	}

// 	httpID := "HTTP_" + uuid.New().String()
// 	key := "lookout_" + httpID
// 	h.Epc.HttpQuery[key] = make(chan []byte, 2)
// 	ep.CtlCustomQuery(query, httpID)

// 	var byteOP []byte
// 	select {
// 	case byteOP = <-h.Epc.HttpQuery[key]:
// 		break
// 	}
// 	return byteOP
// }

// XXX do we need this?
// func (h *CommHandler) QueryHandle(w http.ResponseWriter, r *http.Request) {

// 	//Decode the NISD request structure
// 	requestBytes, err := ioutil.ReadAll(r.Body)
// 	if err != nil {
// 		xlog.Error("ioutil.ReadAll(r.Body):", err)
// 	}

// 	requestObj := requestResponseLib.LookoutRequest{}
// 	dec := gob.NewDecoder(bytes.NewBuffer(requestBytes))
// 	err = dec.Decode(&requestObj)
// 	if err != nil {
// 		xlog.Error("dec.Decode(&requestObj): ", err)
// 	}

// 	//Call the appropriate function
// 	output := h.customQuery(requestObj.UUID, requestObj.Cmd)
// 	//Data to writer
// 	w.Write(output)
// }

func (h *CommHandler) MetricsHandler(w http.ResponseWriter, r *http.Request) {
	//Take snapshot of the EpMap
	epMap := h.Epc.TakeSnapshot()

	for _, ep := range epMap {
		// Only report if not dead
		if ep.State == monitor.EPstateRunning {
			labelMap := make(map[string]string)

			labelMap = ep.App.LoadSystemInfo(labelMap)

			if len(labelMap) > 0 {
				ep.App.Parse(labelMap, w, r)
			}
		}
	}

	// Emit the NVMe capacity/utilization gauges once per unique backing
	// device, tagged with the NISD UUIDs currently sharing it, rather than
	// duplicating the series per-NISD.
	for devPath, entry := range h.Epc.DeviceSSDStatsSnapshot() {
		labels := map[string]string{
			"DEV_PATH":  devPath,
			"NODE_NAME": entry.NodeName,
			"TYPE":      "NISD",
			"NISD_UUID": strings.Join(entry.NisdUUIDs, ","),
		}
		fmt.Fprint(w, ph.GenericPromDataParser(entry.Stats, labels))
	}
}

func (h *CommHandler) LookoutsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	h.mu.Lock()
	jsonData, err := json.MarshalIndent(h.Lookouts, "", "\t")
	h.mu.Unlock()

	if err != nil {
		xlog.Error("Error marshaling Lookouts to JSON: ", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(jsonData)
	if err != nil {
		xlog.Error("Error writing /lookouts response: ", err)
	}
}
