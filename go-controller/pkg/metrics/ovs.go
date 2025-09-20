//go:build linux
// +build linux

package metrics

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"k8s.io/klog/v2"

	libovsdbclient "github.com/ovn-kubernetes/libovsdb/client"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	ovsops "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdb/ops/ovs"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
)

var (
	ovsVersion string
)

// ovs datapath Metrics
var metricOvsDpTotal = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "dp_total",
	Help:      "Represents total number of datapaths on the system.",
})

var metricOvsDp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "dp",
	Help: "A metric with a constant '1' value labeled by datapath " +
		"name present on the instance."},
	[]string{
		"datapath",
		"type",
	},
)

var metricOvsDpIfTotal = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "dp_if_total",
	Help:      "Represents the number of ports connected to the datapath."},
	[]string{
		"datapath",
	},
)

var metricOvsDpFlowsTotal = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "dp_flows_total",
	Help:      "Represents the number of flows in datapath."},
	[]string{
		"datapath",
	},
)

var metricOvsDpFlowsLookupHit = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "dp_flows_lookup_hit",
	Help: "Represents number of packets matching the existing flows " +
		"while processing incoming packets in the datapath."},
	[]string{
		"datapath",
	},
)

var metricOvsDpFlowsLookupMissed = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "dp_flows_lookup_missed",
	Help: "Represents the number of packets not matching any existing " +
		"flow  and require  user space processing."},
	[]string{
		"datapath",
	},
)

var metricOvsDpFlowsLookupLost = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "dp_flows_lookup_lost",
	Help: "number of packets destined for user space process but " +
		"subsequently dropped before  reaching  userspace."},
	[]string{
		"datapath",
	},
)

var metricOvsDpPacketsTotal = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "dp_packets_total",
	Help: "Represents the total number of packets datapath processed " +
		"which is the sum of hit and missed."},
	[]string{
		"datapath",
	},
)

var metricOvsdpMasksHit = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "dp_masks_hit",
	Help:      "Represents the total number of masks visited for matching incoming packets.",
},
	[]string{
		"datapath",
	},
)

var metricOvsDpMasksTotal = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "dp_masks_total",
	Help:      "Represents the number of masks in a datapath."},
	[]string{
		"datapath",
	},
)

var metricOvsDpMasksHitRatio = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "dp_masks_hit_ratio",
	Help: "Represents the average number of masks visited per packet " +
		"the  ratio between hit and total number of packets processed by the datapath."},
	[]string{
		"datapath",
	},
)

// ovs bridge statistics & attributes metrics
var metricOvsBridgeTotal = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "bridge_total",
	Help:      "Represents total number of OVS bridges on the system.",
},
)

var metricOvsBridge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "bridge",
	Help: "A metric with a constant '1' value labeled by bridge name " +
		"present on the instance."},
	[]string{
		"bridge",
	},
)

var metricOvsBridgePortsTotal = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "bridge_ports_total",
	Help:      "Represents the number of OVS ports on the bridge."},
	[]string{
		"bridge",
	},
)

var metricOvsBridgeFlowsTotal = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "bridge_flows_total",
	Help:      "Represents the number of OpenFlow flows on the OVS bridge."},
	[]string{
		"bridge",
	},
)

// ovs interface metrics
var metricOvsInterfaceResetsTotal = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "interface_resets_total",
	Help:      "The number of link state changes observed by Open vSwitch interface(s).",
})

var metricOvsInterfaceRxDroppedTotal = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "interface_rx_dropped_total",
	Help:      "The total number of received packets dropped by Open vSwitch interface(s).",
})

var metricOvsInterfaceTxDroppedTotal = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "interface_tx_dropped_total",
	Help:      "The total number of transmitted packets dropped by Open vSwitch interface(s).",
})

var metricOvsInterfaceRxErrorsTotal = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "interface_rx_errors_total",
	Help:      "The total number of received packets with errors by Open vSwitch interface(s).",
})

var metricOvsInterfaceTxErrorsTotal = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "interface_tx_errors_total",
	Help:      "The total number of transmitted packets with errors by Open vSwitch interface(s).",
})

var metricOvsInterfaceCollisionsTotal = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "interface_collisions_total",
	Help:      "The total number of packet collisions transmitted by Open vSwitch interface(s).",
})

var metricOvsInterfaceTotal = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "interfaces_total",
	Help:      "The total number of Open vSwitch interface(s) created for pods",
})

var MetricOvsInterfaceUpWait = prometheus.NewCounter(prometheus.CounterOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "interface_up_wait_seconds_total",
	Help: "The total number of seconds that is required to wait for pod " +
		"Open vSwitch interface until its available",
})

// ovs memory metrics
var metricOvsHandlersTotal = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "handlers_total",
	Help: "Represents the number of handlers thread. This thread reads upcalls from dpif, " +
		"forwards each upcall's packet and possibly sets up a kernel flow as a cache.",
})

var metricOvsRevalidatorsTotal = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "revalidators_total",
	Help: "Represents the number of revalidators thread. This thread processes datapath flows, " +
		"updates OpenFlow statistics, and updates or removes them if necessary.",
})

// ovs Hw offload metrics
var metricOvsHwOffload = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "hw_offload",
	Help: "Represents whether netdev flow offload to hardware is enabled " +
		"or not -- false(0) and true(1).",
})

var metricOvsTcPolicy = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: types.MetricOvsNamespace,
	Subsystem: types.MetricOvsSubsystemVswitchd,
	Name:      "tc_policy",
	Help: "Represents the policy used with HW offloading " +
		"-- none(0), skip_sw(1), and skip_hw(2).",
})

type ovsClient func(args ...string) (string, string, error)

func convertToFloat64(val *int) float64 {
	var value float64
	if val != nil {
		value = float64(*val)
	} else {
		value = 0
	}
	return value
}

<<<<<<< HEAD
func getOvsVersionInfo(ovsDBClient libovsdbclient.Client) {
	openvSwitch, err := ovsops.GetOpenvSwitch(ovsDBClient)
=======
func OvsVersionInfoUpdater(ovsDBClient libovsdbclient.Client, nodeName string, metricsScrapeInterval int, stopChan <-chan struct{}) {
	ticker := time.NewTicker(time.Duration(metricsScrapeInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := getOvsVersionInfo(nodeName, ovsDBClient); err != nil {
				klog.Errorf("Error getting ovs version: %v", err)
			}
		case <-stopChan:
			return
		}
	}
}

func getOvsVersionInfo(nodeName string, ovsDBClient libovsdbclient.Client) (err error) {
	klog.Infof("XXXXX getOvsVersionInfo")
	metricOvsVersion.Reset()
	openVswitch, err := ovsops.GetOpenvSwitch(ovsDBClient)
>>>>>>> b47f8f274 (update)
	if err != nil {
		klog.Errorf("Failed to get ovsdb openvswitch entry :(%v)", err)
		return
	}
<<<<<<< HEAD
	if openvSwitch.OVSVersion != nil {
		ovsVersion = *openvSwitch.OVSVersion
=======
	klog.Infof("XXXXX openVswitch.OVSVersion: %s", *openVswitch.OVSVersion)
	if openVswitch.OVSVersion != nil {
		ovsVersion := *openVswitch.OVSVersion
		metricOvsVersion.WithLabelValues(ovsVersion, nodeName).Set(1)
>>>>>>> b47f8f274 (update)
	} else {
		klog.Errorf("Failed to get ovs version information")
		return
	}
}

// ovsDatapathLookupsMetrics obtains the ovs datapath
// (lookups: hit, missed, lost) metrics and updates them.
func ovsDatapathLookupsMetrics(output, datapath string) {
	var datapathPacketsTotal float64
	for _, field := range strings.Fields(output) {
		elem := strings.Split(field, ":")
		if len(elem) != 2 {
			continue
		}
		switch elem[0] {
		case "hit":
			value := parseMetricToFloat(types.MetricOvsSubsystemVswitchd, "dp_flows_lookup_hit", elem[1])
			datapathPacketsTotal += value
			metricOvsDpFlowsLookupHit.WithLabelValues(datapath).Set(value)
		case "missed":
			value := parseMetricToFloat(types.MetricOvsSubsystemVswitchd, "dp_flows_lookup_missed", elem[1])
			datapathPacketsTotal += value
			metricOvsDpFlowsLookupMissed.WithLabelValues(datapath).Set(value)
		case "lost":
			value := parseMetricToFloat(types.MetricOvsSubsystemVswitchd, "dp_flows_lookup_lost", elem[1])
			metricOvsDpFlowsLookupLost.WithLabelValues(datapath).Set(value)
		}
	}
	metricOvsDpPacketsTotal.WithLabelValues(datapath).Set(datapathPacketsTotal)
}

// ovsDatapathMasksMetrics obatins ovs datapath masks metrics
// (masks :hit, total, hit/pkt) and updates them.
func ovsDatapathMasksMetrics(output, datapath string) {
	for _, field := range strings.Fields(output) {
		elem := strings.Split(field, ":")
		if len(elem) != 2 {
			continue
		}
		switch elem[0] {
		case "hit":
			value := parseMetricToFloat(types.MetricOvsSubsystemVswitchd, "dp_masks_hit", elem[1])
			metricOvsdpMasksHit.WithLabelValues(datapath).Set(value)
		case "total":
			value := parseMetricToFloat(types.MetricOvsSubsystemVswitchd, "dp_masks_total", elem[1])
			metricOvsDpMasksTotal.WithLabelValues(datapath).Set(value)
		case "hit/pkt":
			value := parseMetricToFloat(types.MetricOvsSubsystemVswitchd, "dp_masks_hit_ratio", elem[1])
			metricOvsDpMasksHitRatio.WithLabelValues(datapath).Set(value)
		}
	}
}

// getOvsDatapaths gives list of datapaths
// and updates the corresponding datapath metrics
func getOvsDatapaths(ovsAppctl ovsClient) (datapathsList []string, err error) {
	var stdout, stderr string

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recovering from a panic while parsing the "+
				"ovs-appctl dpctl/dump-dps output : %v", r)
		}
	}()

	stdout, stderr, err = ovsAppctl("dpctl/dump-dps")
	if err != nil {
		return nil, fmt.Errorf("failed to get output of ovs-appctl dpctl/dump-dps "+
			"stderr(%s) :(%v)", stderr, err)
	}
	for _, kvPair := range strings.Split(stdout, "\n") {
		var datapathType, datapathName string
		output := strings.TrimSpace(kvPair)
		if strings.Contains(output, "@") {
			datapath := strings.Split(output, "@")
			datapathType, datapathName = datapath[0], datapath[1]
		} else {
			return nil, fmt.Errorf("datapath %s is not of format Type@Name", output)
		}
		metricOvsDp.WithLabelValues(datapathName, datapathType).Set(1)
		datapathsList = append(datapathsList, output)
	}
	metricOvsDpTotal.Set(float64(len(datapathsList)))
	return datapathsList, nil
}

func setOvsDatapathMetrics(ovsAppctl ovsClient, datapaths []string) (err error) {
	var stdout, stderr, datapath string

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recovering from a panic while parsing the ovs-appctl dpctl/"+
				"show %s output : %v", datapath, r)
		}
	}()

	for _, datapath = range datapaths {
		// For example, datapath is 'system@ovs-system' where 'system' denotes
		// the datapath type and 'ovs-system' the datapath name. To uniquely
		// identify a datapath, both are required when querying OVS. If type is
		// omitted, OVS will assume 'system'.
		stdout, stderr, err = ovsAppctl("dpctl/show", datapath)
		if err != nil {
			return fmt.Errorf("failed to get datapath stats for %s "+
				"stderr(%s) :(%v)", datapath, stderr, err)
		}

		// For metrics, only a datapath name will be used to identify datapaths
		// in order to keep backward compatibility with previous behaviour.
		datapathName := strings.Split(datapath, "@")[1]
		var datapathPortCount float64
		for i, kvPair := range strings.Split(stdout, "\n") {
			if i <= 0 {
				// skip the first line which is datapath name
				continue
			}
			output := strings.TrimSpace(kvPair)
			if strings.HasPrefix(output, "lookups:") {
				ovsDatapathLookupsMetrics(output, datapathName)
			} else if strings.HasPrefix(output, "masks:") {
				ovsDatapathMasksMetrics(output, datapathName)
			} else if strings.HasPrefix(output, "port ") {
				datapathPortCount++
			} else if strings.HasPrefix(output, "flows:") {
				flowFields := strings.Fields(output)
				value := parseMetricToFloat(types.MetricOvsSubsystemVswitchd, "dp_flows_total", flowFields[1])
				metricOvsDpFlowsTotal.WithLabelValues(datapathName).Set(value)
			}
		}
		metricOvsDpIfTotal.WithLabelValues(datapathName).Set(datapathPortCount)
	}
	return nil
}

<<<<<<< HEAD
=======
func setOvsDatapathOffloadMetrics(ovsVswitchdAppctl ovsClient) error {
	stdout, stderr, err := ovsVswitchdAppctl("upcall/show")
	if err != nil {
		return fmt.Errorf("failed to get output of ovs-appctl upcall/show "+
			"stderr(%s) :(%v)", stderr, err)
	}

	output := strings.Split(stdout, "\n")
	var datapathName string
	for _, line := range output {
		if strings.Contains(line, "@") {
			datapath := strings.Split(line, "@")
			datapathName = strings.TrimSuffix(datapath[1], ":")
		} else if strings.Contains(line, "offloaded flows") {
			offloadFields := strings.Split(line, ":")
			offloadValue := strings.TrimSpace(offloadFields[1])
			value := parseMetricToFloat(MetricOvsSubsystemVswitchd, "dp_offloaded_flows_total", offloadValue)
			metricOvsDpOffloadedFlowsTotal.WithLabelValues(datapathName).Set(value)
			break
		}
	}
	return nil
}

func updateOvsDatapathMetrics(ovsVswitchdAppctl ovsClient) {
	datapaths, err := getOvsDatapaths(ovsVswitchdAppctl)
	if err != nil {
		klog.Errorf("Getting ovs datapath list failed: %s", err.Error())
		return
	}

	if err = setOvsDatapathMetrics(ovsVswitchdAppctl, datapaths); err != nil {
		klog.Errorf("Setting ovs datapath metrics failed: %s", err.Error())
	}
	if err = setOvsDatapathOffloadMetrics(ovsVswitchdAppctl); err != nil {
		klog.Errorf("Setting ovs datapath offload metrics failed: %s", err.Error())
	}
}

>>>>>>> b47f8f274 (update)
// ovsDatapathMetricsUpdater updates the ovs datapath metrics
func ovsDatapathMetricsUpdater(ovsAppctl ovsClient, metricsScrapeInterval int, stopChan <-chan struct{}) {
	ticker := time.NewTicker(time.Duration(metricsScrapeInterval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
<<<<<<< HEAD
			datapaths, err := getOvsDatapaths(ovsAppctl)
			if err != nil {
				klog.Errorf("Getting ovs datapath list failed: %s", err.Error())
				continue
			}
			if err = setOvsDatapathMetrics(ovsAppctl, datapaths); err != nil {
				klog.Errorf("Setting ovs datapath metrics failed: %s", err.Error())
			}
=======
			klog.Infof("XXXXX ovsDatapathMetricsUpdater")
			updateOvsDatapathMetrics(ovsVswitchdAppctl)
>>>>>>> b47f8f274 (update)
		case <-stopChan:
			return
		}
	}
}

func resetOvsBridgeMetrics() {
	// we need to reset metrics vectors prior to collecting new ones.
	// this reset is local to prom client endpoint only and helps us
	// improve performance by deleting non-actual stale metrics
	metricInterfaceDriverName.Reset()
	metricInterfaceDriverVersion.Reset()
	metricInterfaceFirmwareVersion.Reset()
	for _, interfaceMetricInfo := range ovsInterfaceMetricsDataMap {
		interfaceMetricInfo.metric.Reset()
	}

}

// ovsBridgeMetricsUpdater updates bridge related metrics
func ovsBridgeMetricsUpdater(ovsDBClient libovsdbclient.Client, ovsOfctl ovsClient, metricsScrapeInterval int, stopChan <-chan struct{}) {
	ticker := time.NewTicker(time.Duration(metricsScrapeInterval) * time.Second)
	defer ticker.Stop()
	var err error
	for {
		select {
		case <-ticker.C:
<<<<<<< HEAD
			if err = updateOvsBridgeMetrics(ovsDBClient, ovsAppctl); err != nil {
=======
			klog.Infof("XXXXX ovsBridgeMetricsUpdater")
			resetOvsBridgeMetrics()
			// set geneve interface metrics
			err := geneveInterfaceMetricsUpdate()
			if err != nil {
				klog.Errorf("%s", err.Error())
			}
			// update ovs bridge metrics
			if err = updateOvsBridgeMetrics(ovsDBClient, ovsOfctl); err != nil {
>>>>>>> b47f8f274 (update)
				klog.Errorf("Getting ovs bridge info failed: %s", err.Error())
			}
		case <-stopChan:
			return
		}
	}
}

func updateOvsBridgeMetrics(ovsDBClient libovsdbclient.Client, ovsOfctl ovsClient) error {
	bridgeList, err := ovsops.ListBridges(ovsDBClient)
	if err != nil {
		return fmt.Errorf("failed to get ovsdb bridge table :(%v)", err)
	}
	metricOvsBridgeTotal.Set(float64(len(bridgeList)))
	for _, bridge := range bridgeList {
		brName := bridge.Name
		metricOvsBridge.WithLabelValues(brName).Set(1)
		flowsCount, err := getOvsBridgeOpenFlowsCount(ovsOfctl, brName)
		if err != nil {
			return err
		}
		metricOvsBridgeFlowsTotal.WithLabelValues(brName).Set(flowsCount)
		metricOvsBridgePortsTotal.WithLabelValues(brName).Set(float64(len(bridge.Ports)))
	}

	return nil
}

// getOvsBridgeOpenFlowsCount returns the number of openflow flows
// in an ovs-bridge
func getOvsBridgeOpenFlowsCount(ovsOfctl ovsClient, bridgeName string) (float64, error) {
	stdout, stderr, err := ovsOfctl("-t", "5", "dump-aggregate", bridgeName)
	if err != nil {
		return 0, fmt.Errorf("failed to get flow count for %s, stderr(%s): (%v)",
			bridgeName, stderr, err)
	}
	if stderr != "" {
		return 0, fmt.Errorf("failed to get OVS flow for %s due to stderr: %s", bridgeName, stderr)
	}
	if stdout == "" {
		return 0, fmt.Errorf("unable to update OVS bridge open flow count metric because blank output received from OVS client")
	}
	for _, kvPair := range strings.Fields(stdout) {
		if strings.HasPrefix(kvPair, "flow_count=") {
			value := strings.Split(kvPair, "=")[1]
			metricName := bridgeName + "flows_total"
			return parseMetricToFloat(types.MetricOvsSubsystemVswitchd, metricName, value), nil
		}
	}
	return 0, fmt.Errorf("ovs-ofctl dump-aggregate %s output didn't contain "+
		"flow_count field", bridgeName)
}

<<<<<<< HEAD
func ovsInterfaceMetricsUpdater(ovsDBClient libovsdbclient.Client, metricsScrapeInterval int, stopChan <-chan struct{}) {
	ticker := time.NewTicker(time.Duration(metricsScrapeInterval) * time.Second)
	defer ticker.Stop()
=======
func registerOvsInterfaceMetrics(metricNamespace, metricSubsystem string) {
	// The metrics not covered by the OVS native metrics are moved to ovsInterfaceExtraMetricsDataMap,
	// so the ovsInterfaceExtraMetricsDataMap can be used to register them when OVS native metrics is enabled.
	// This function is only called when OVS native metrics is disabled, so merge them into ovsInterfaceMetricsDataMap to
	// make it backward compatible.
	for metricName, metricInfo := range ovsInterfaceExtraMetricsDataMap {
		ovsInterfaceMetricsDataMap[metricName] = metricInfo
	}

	for InterfaceMetricName, InterfaceMetricInfo := range ovsInterfaceMetricsDataMap {
		InterfaceMetricInfo.metric = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: metricSubsystem,
			Name:      InterfaceMetricName,
			Help:      InterfaceMetricInfo.help,
		},
			[]string{
				"bridge",
				"port",
				"interface",
			})
		prometheus.MustRegister(InterfaceMetricInfo.metric)
	}
}

func getOvsInterfaceType(state string) float64 {
	var typeValue float64
	if state == "" {
		state = "system"
	}
	interfaceTypeMap := map[string]float64{
		"system":   1,
		"internal": 2,
		"tap":      3,
		"geneve":   4,
		"gre":      5,
		"vxlan":    6,
		"lisp":     7,
		"stt":      8,
		"patch":    9,
		"dpdk":     10,
	}
	if value, ok := interfaceTypeMap[state]; ok {
		typeValue = value
	} else {
		typeValue = 0
	}
	return typeValue
}

func getOvsInterfaceDuplexType(fieldValue *string) float64 {
	var duplexValue float64
	duplexValue = 2
	if fieldValue != nil {
		if *fieldValue == "half" {
			duplexValue = 0
		} else if *fieldValue == "full" {
			duplexValue = 1
		}
	}
	return duplexValue
}

func getOvsInterfaceState(state *string) float64 {
	var stateValue float64
	if state == nil || *state == "" {
		return 0
	}
	stateMap := map[string]float64{
		"down": 1,
		"up":   2,
	}
	if value, ok := stateMap[*state]; ok {
		stateValue = value
	} else {
		stateValue = 0
	}
	return stateValue
}

func setOvsInterfaceStatistics(interfaceBridge, interfacePort, interfaceName string,
	statsMap map[string]int) {
	var InterfaceStats = []string{
		"rx_packets",
		"rx_bytes",
		"rx_dropped",
		"rx_frame_err",
		"rx_over_err",
		"rx_crc_err",
		"rx_errors",
		"tx_packets",
		"tx_bytes",
		"tx_dropped",
		"collisions",
		"tx_errors",
	}

	for _, stat := range InterfaceStats {
		var statValue float64
		metricName := "interface_" + stat
		if value, ok := statsMap[stat]; ok {
			statValue = float64(value)
		}
		ovsInterfaceMetricsDataMap[metricName].metric.WithLabelValues(interfaceBridge,
			interfacePort, interfaceName).Set(statValue)
	}
}

func setSriovInterfaceStatsViaEthtool(etHandler *ethtool.Ethtool, interfaceBridge, interfacePort, interfaceName,
	interfaceDriverName string) {
	// Check if this is Representor, skip anything else
	// For ovs-doca the driver_name is mlx5_pci whereas for ovs-kernel the driver_name is mxl5e_rep
	if etHandler == nil || !strings.HasPrefix(interfaceDriverName, "mlx5") {
		// Check if we need to explicitly set these to 0
		return
	}

	ethStats, err := etHandler.Stats(interfaceName)
	if err != nil {
		klog.Errorf("Failed to get stats using ethtool binding: %v", err)
		return
	}

	// VF-rep and uplink interface have different stats group to count bytes/packets,
	// see https://docs.kernel.org/networking/device_drivers/ethernet/mellanox/mlx5/counters.html
	getStatWithFallback := func(k1 string, k2 string) uint64 {
		if v, ok := ethStats[k1]; ok {
			return v
		}
		if v, ok := ethStats[k2]; ok {
			return v
		}
		return 0
	}

	swRxBytes := ethStats["tx_bytes"]
	swRxPackets := ethStats["tx_packets"]
	totalRxBytes := getStatWithFallback("vport_tx_bytes", "tx_bytes_phy")
	totalRxPackets := getStatWithFallback("vport_tx_packets", "tx_packets_phy")

	// Pod Transmit is VF-Representor receive
	swTxBytes := ethStats["rx_bytes"]
	swTxPackets := ethStats["rx_packets"]
	totalTxBytes := getStatWithFallback("vport_rx_bytes", "rx_bytes_phy")
	totalTxPackets := getStatWithFallback("vport_rx_packets", "rx_packets_phy")

	ovsInterfaceMetricsDataMap["interface_tx_sw_bytes"].metric.WithLabelValues(
		interfaceBridge, interfacePort, interfaceName).Set(float64(swTxBytes))
	ovsInterfaceMetricsDataMap["interface_tx_total_bytes"].metric.WithLabelValues(
		interfaceBridge, interfacePort, interfaceName).Set(float64(totalTxBytes))
	ovsInterfaceMetricsDataMap["interface_tx_sw_packets"].metric.WithLabelValues(
		interfaceBridge, interfacePort, interfaceName).Set(float64(swTxPackets))
	ovsInterfaceMetricsDataMap["interface_tx_total_packets"].metric.WithLabelValues(
		interfaceBridge, interfacePort, interfaceName).Set(float64(totalTxPackets))

	ovsInterfaceMetricsDataMap["interface_rx_sw_bytes"].metric.WithLabelValues(
		interfaceBridge, interfacePort, interfaceName).Set(float64(swRxBytes))
	ovsInterfaceMetricsDataMap["interface_rx_total_bytes"].metric.WithLabelValues(
		interfaceBridge, interfacePort, interfaceName).Set(float64(totalRxBytes))
	ovsInterfaceMetricsDataMap["interface_rx_sw_packets"].metric.WithLabelValues(
		interfaceBridge, interfacePort, interfaceName).Set(float64(swRxPackets))
	ovsInterfaceMetricsDataMap["interface_rx_total_packets"].metric.WithLabelValues(
		interfaceBridge, interfacePort, interfaceName).Set(float64(totalRxPackets))
}

func setOvsPortMissPktsInfo(interfaceBridge, interfacePort, interfaceName, interfaceDriverName string) {
	// Check if this is Representor, if so, let's get any rate configured on it.
	if strings.Compare(interfaceDriverName, "mlx5e_rep") != 0 {
		// Check if we need to explicitly set these to 0
		return
	}
	// In theory we don't need to get this information every time, but this value can
	// change, so we can read it along with the dropped stats.
	maxPPS, burstPPS, err := util.GetSriovnetOps().GetRepresentorVFMissPktRate(interfaceName)
	if err != nil {
		// Maybe set some value that indicates  "unknown" and trigger an alert based on that.
		klog.Errorf("setOvsPortMissPktsInfo: error getting misspkt rate..: %v", err)
		return
	}
	ovsInterfaceMetricsDataMap["interface_tx_misspkts_pps"].metric.WithLabelValues(
		interfaceBridge, interfacePort, interfaceName).Set(float64(maxPPS))
	ovsInterfaceMetricsDataMap["interface_tx_misspkts_burst"].metric.WithLabelValues(
		interfaceBridge, interfacePort, interfaceName).Set(float64(burstPPS))
	dropPacktCount, err := util.GetSriovnetOps().GetRepresentorVFMissPktDrops(interfaceName)
	if err != nil {
		// Maybe set some value that indicates  "unknown" and trigger an alert based on that.
		klog.Errorf("setOvsPortMissPktsInfo: error getting misspkt drops..: %v", err)
		return
	}
	ovsInterfaceMetricsDataMap["interface_tx_misspkts_packets_drops"].metric.WithLabelValues(
		interfaceBridge, interfacePort, interfaceName).Set(float64(dropPacktCount))
}

func setOvsInterfaceQdiscIngress(interfaceName string, bridgeName string, portName string,
	link netlink.Link) {
	var metricValue float64 = -1
>>>>>>> b47f8f274 (update)
	var err error
	for {
		select {
		case <-ticker.C:
			if err = updateOvsInterfaceMetrics(ovsDBClient); err != nil {
				klog.Errorf("Updating OVS interface metrics failed: %s", err.Error())
			}
		case <-stopChan:
			return
		}
	}
}

// updateOvsInterfaceMetrics updates the ovs interface metrics obtained from ovsdb
func updateOvsInterfaceMetrics(ovsDBClient libovsdbclient.Client) error {
	interfaceList, err := ovsops.ListInterfaces(ovsDBClient)
	if err != nil {
		return fmt.Errorf("failed to get ovsdb interface table :(%v)", err)
	}
<<<<<<< HEAD
	var interfaceStats = []string{
		"rx_dropped",
		"rx_errors",
		"tx_dropped",
		"tx_errors",
		"collisions",
=======
	for _, interfaceInfo := range interfaceList {
		interfaceName := interfaceInfo.Name
		interfaceData := interfaceInfoMap[interfaceInfo.UUID]
		interfaceTypeValue := getOvsInterfaceType(interfaceInfo.Type)
		if interfaceTypeValue == 0 || interfaceTypeValue == 4 {
			// not gathering metrics for not-typed and geneve interfaces
			continue
		}
		portName := interfaceData.port
		if ifaceID, ok := interfaceInfo.ExternalIDs["iface-id"]; ok {
			portName = ifaceID
		}
		if !config.Default.EnableOvsNativeMetrics {
			ovsInterfaceMetricsDataMap["interface_type"].metric.WithLabelValues(
				interfaceData.bridge, portName, interfaceName).Set(interfaceTypeValue)
			duplexType := getOvsInterfaceDuplexType(interfaceInfo.Duplex)
			ovsInterfaceMetricsDataMap["interface_duplex"].metric.WithLabelValues(
				interfaceData.bridge, portName, interfaceName).Set(duplexType)
			adminStateValue := getOvsInterfaceState(interfaceInfo.AdminState)
			ovsInterfaceMetricsDataMap["interface_admin_state"].metric.WithLabelValues(
				interfaceData.bridge, portName, interfaceName).Set(adminStateValue)
			linkStatevalue := getOvsInterfaceState(interfaceInfo.LinkState)
			ovsInterfaceMetricsDataMap["interface_link_state"].metric.WithLabelValues(
				interfaceData.bridge, portName, interfaceName).Set(linkStatevalue)
			ovsInterfaceMetricsDataMap["interface_ifindex"].metric.WithLabelValues(
				interfaceData.bridge, portName, interfaceName).Set(convertToFloat64(interfaceInfo.Ifindex))
			ovsInterfaceMetricsDataMap["interface_link_resets"].metric.WithLabelValues(
				interfaceData.bridge, portName, interfaceName).Set(convertToFloat64(interfaceInfo.LinkResets))
			ovsInterfaceMetricsDataMap["interface_link_speed"].metric.WithLabelValues(
				interfaceData.bridge, portName, interfaceName).Set(convertToFloat64(interfaceInfo.LinkSpeed))
			ovsInterfaceMetricsDataMap["interface_mtu"].metric.WithLabelValues(
				interfaceData.bridge, portName, interfaceName).Set(convertToFloat64(interfaceInfo.MTU))
			ovsInterfaceMetricsDataMap["interface_of_port"].metric.WithLabelValues(
				interfaceData.bridge, portName, interfaceName).Set(convertToFloat64(interfaceInfo.Ofport))
			ovsInterfaceMetricsDataMap["interface_ingress_policing_burst"].metric.WithLabelValues(
				interfaceData.bridge, portName, interfaceName).Set(float64(interfaceInfo.IngressPolicingBurst))
			ovsInterfaceMetricsDataMap["interface_ingress_policing_rate"].metric.WithLabelValues(
				interfaceData.bridge, portName, interfaceName).Set(float64(interfaceInfo.IngressPolicingRate))
			// set the ovs interface status fields
			setOvsInterfaceStatusFields(interfaceData.bridge, portName, interfaceName, interfaceInfo.Status)
			// set ovs interface stastics fields
			setOvsInterfaceStatistics(interfaceData.bridge, portName, interfaceName, interfaceInfo.Statistics)
		}

		if interfaceTypeValue != 2 && interfaceTypeValue != 9 && interfaceTypeValue != 10 {
			setOvsInterfaceQdiscIngress(interfaceName, interfaceData.bridge, portName, nil)
		}
		// set interface limits, if any, on the number of new connections (i.e. missed packets)  initiated.
		setOvsPortMissPktsInfo(interfaceData.bridge, portName, interfaceName, interfaceInfo.Status["driver_name"])
		// set SR-IOV interface stats.
		setSriovInterfaceStatsViaEthtool(etHandler, interfaceData.bridge, portName, interfaceName, interfaceInfo.Status["driver_name"])
>>>>>>> b47f8f274 (update)
	}

	var linkReset, rxDropped, txDropped, rxErr, txErr, collisions, statValue float64
	for _, intf := range interfaceList {
		linkReset += convertToFloat64(intf.LinkResets)

		for _, statName := range interfaceStats {
			statValue = 0
			if value, ok := intf.Statistics[statName]; ok {
				statValue = float64(value)
			}
			switch statName {
			case "rx_dropped":
				rxDropped += statValue
			case "tx_dropped":
				txDropped += statValue
			case "rx_errors":
				rxErr += statValue
			case "tx_errors":
				txErr += statValue
			case "collisions":
				collisions += statValue
			}
		}
	}
	metricOvsInterfaceTotal.Set(float64(len(interfaceList)))
	metricOvsInterfaceResetsTotal.Set(linkReset)
	metricOvsInterfaceRxDroppedTotal.Set(rxDropped)
	metricOvsInterfaceTxDroppedTotal.Set(txDropped)
	metricOvsInterfaceRxErrorsTotal.Set(rxErr)
	metricOvsInterfaceTxErrorsTotal.Set(txErr)
	metricOvsInterfaceCollisionsTotal.Set(collisions)
	return nil
}

// setOvsMemoryMetrics updates the handlers, revalidators
// count from "ovs-appctl -t ovs-vswitchd memory/show" output.
func setOvsMemoryMetrics(ovsVswitchdAppctl ovsClient) (err error) {
	var stdout, stderr string

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recovering from panic while parsing the ovs-appctl "+
				"memory/show output : %v", r)
		}
	}()

	stdout, stderr, err = ovsVswitchdAppctl("memory/show")
	if err != nil {
		return fmt.Errorf("failed to retrieve memory/show output "+
			"for ovs-vswitchd stderr(%s) :%v", stderr, err)
	}

	for _, kvPair := range strings.Fields(stdout) {
		if strings.HasPrefix(kvPair, "handlers:") {
			value := strings.Split(kvPair, ":")[1]
			count := parseMetricToFloat(types.MetricOvsSubsystemVswitchd, "handlers_total", value)
			metricOvsHandlersTotal.Set(count)
		} else if strings.HasPrefix(kvPair, "revalidators:") {
			value := strings.Split(kvPair, ":")[1]
			count := parseMetricToFloat(types.MetricOvsSubsystemVswitchd, "revalidators_total", value)
			metricOvsRevalidatorsTotal.Set(count)
		}
	}
	return nil
}

func ovsMemoryMetricsUpdater(ovsVswitchdAppctl ovsClient, metricsScrapeInterval int, stopChan <-chan struct{}) {
	ticker := time.NewTicker(time.Duration(metricsScrapeInterval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			klog.Infof("XXXXX ovsMemoryMetricsUpdater")
			if err := setOvsMemoryMetrics(ovsVswitchdAppctl); err != nil {
				klog.Errorf("Setting ovs memory metrics failed: %s", err.Error())
			}
		case <-stopChan:
			return
		}
	}
}

// setOvsHwOffloadMetrics updates the hw-offload, tc-policy metrics
// obtained from Open_vSwitch table updates
func setOvsHwOffloadMetrics(ovsDBClient libovsdbclient.Client) (err error) {
	openvSwitch, err := ovsops.GetOpenvSwitch(ovsDBClient)
	if err != nil {
		return fmt.Errorf("failed to get ovsdb openvswitch entry :(%v)", err)
	}
	var hwOffloadValue = "false"
	var tcPolicyValue = "none"
	var tcPolicyMap = map[string]float64{
		"none":    0,
		"skip_sw": 1,
		"skip_hw": 2,
	}

	// set the hw-offload metric
	if val, ok := openvSwitch.OtherConfig["hw-offload"]; ok {
		hwOffloadValue = val
	}
	if hwOffloadValue == "false" {
		metricOvsHwOffload.Set(0)
	} else {
		metricOvsHwOffload.Set(1)
	}
	// set tc-policy metric
	if val, ok := openvSwitch.OtherConfig["tc-policy"]; ok {
		tcPolicyValue = val
	}
	metricOvsTcPolicy.Set(tcPolicyMap[tcPolicyValue])
	return nil
}

func ovsHwOffloadMetricsUpdater(ovsDBClient libovsdbclient.Client, metricsScrapeInterval int, stopChan <-chan struct{}) {
	ticker := time.NewTicker(time.Duration(metricsScrapeInterval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			klog.Infof("XXXXX ovsHwOffloadMetricsUpdater")
			if err := setOvsHwOffloadMetrics(ovsDBClient); err != nil {
				klog.Errorf("Setting ovs hardware offload metrics failed: %s", err.Error())
			}
		case <-stopChan:
			return
		}
	}
}

<<<<<<< HEAD
=======
type ovsInterfaceMetricsDetails struct {
	help   string
	metric *prometheus.GaugeVec
}

var ovsInterfaceExtraMetricsDataMap = map[string]*ovsInterfaceMetricsDetails{
	"interface_tx_misspkts_packets_drops": {
		help: "Represents the number of new connection packets dropped " +
			"by the hardware.",
	},
	"interface_tx_misspkts_pps": {
		help: "Maximum rate of allowed new connections on OVS interface, " +
			"in pps. If the value is 0, then rate is disabled.",
	},
	"interface_tx_misspkts_burst": {
		help: "Maximum burst size of allowed new connections on OVS interface, " +
			"in pps.",
	},
	"interface_ingress_qdisc_total": {
		help: "Denotes the total ingress filters on the device",
	},
	// stats from ethtool -S
	"interface_tx_sw_bytes": {
		help: "Sent bytes via software OVS path",
	},
	"interface_tx_total_bytes": {
		help: "Sent bytes in total via net interface",
	},
	"interface_tx_sw_packets": {
		help: "Sent packets via software OVS path",
	},
	"interface_tx_total_packets": {
		help: "Sent packets in total via net interface",
	},
	"interface_rx_sw_bytes": {
		help: "Received bytes via software OVS path",
	},
	"interface_rx_total_bytes": {
		help: "Received bytes in total via net interface",
	},
	"interface_rx_sw_packets": {
		help: "Received packets via software OVS path",
	},
	"interface_rx_total_packets": {
		help: "Received packets in total via net interface",
	},
	// genev_sys_6081 emits below ovs interface metrics based on
	// `ip -s li show genev_sys_6081` output
	// note that, label bridge and port are set to 'none' for genev_sys_6081
	"interface_link_state": {
		help: "The link state of the OVS interface. " +
			"The values are: down(1) or up(2) or other(0).",
	},
	"interface_mtu": {
		help: "The currently configured MTU for OVS interface.",
	},
	"interface_ifindex": {
		help: "Represents the interface index associated with OVS interface.",
	},
	"interface_xxxxxxxxxxx": {
		help: "Represents the interface index associated with OVS interface.",
	},
	"interface_rx_packets": {
		help: "Represents the number of received packets " +
			"by OVS interface.",
	},
	"interface_rx_bytes": {
		help: "Represents the number of received bytes by " +
			"OVS interface.",
	},
	"interface_rx_dropped": {
		help: "Represents the number of input packets dropped " +
			"by OVS interface.",
	},
	"interface_rx_frame_err": {
		help: "Represents the number of frame alignment errors " +
			"on the packets received by OVS interface.",
	},
	"interface_rx_over_err": {
		help: "Represents the number of packets with RX overrun " +
			"received by OVS interface.",
	},
	"interface_rx_crc_err": {
		help: "Represents the number of CRC errors for the packets " +
			"received by OVS interface.",
	},
	"interface_rx_errors": {
		help: "Represents the total number of packets with errors " +
			"received by OVS interface.",
	},
	"interface_tx_packets": {
		help: "Represents the number of transmitted packets by " +
			"OVS interface.",
	},
	"interface_tx_bytes": {
		help: "Represents the number of transmitted bytes " +
			"by OVS interface.",
	},
	"interface_tx_dropped": {
		help: "Represents the number of output packets dropped " +
			"by OVS interface.",
	},
	"interface_collisions": {
		help: "Represents the number of collisions " +
			"on the packets transmitted by OVS interface.",
	},
	"interface_tx_errors": {
		help: "Represents the total number of packets with errors " +
			"transmitted by OVS interface.",
	},
}

var ovsInterfaceMetricsDataMap = map[string]*ovsInterfaceMetricsDetails{
	// Not adding bytes currently, as the packets stats should suffice for
	// this metric.
	"interface_ingress_policing_rate": {
		help: "Maximum rate for data received on OVS interface, " +
			"in kbps. If the value is 0, then policing is disabled.",
	},
	"interface_ingress_policing_burst": {
		help: "Maximum burst size for data received on OVS interface, " +
			"in kb. The default burst size if set to 0 is 8000 kbit.",
	},
	"interface_admin_state": {
		help: "The administrative state of the OVS interface. " +
			"The values are: other(0), down(1) or up(2).",
	},
	"interface_type": {
		help: "Represents the interface type other(0), system(1), internal(2), " +
			"tap(3), geneve(4), gre(5), vxlan(6), lisp(7), stt(8), patch(9).",
	},
	"interface_of_port": {
		help: "Represents the OpenFlow port ID associated with OVS interface.",
	},
	"interface_duplex": {
		help: "The duplex mode of the OVS interface. The values are half(0) " +
			"or full(1) or other(2)",
	},
	"interface_link_speed": {
		help: "The negotiated speed of the OVS interface.",
	},
	"interface_link_resets": {
		help: "The number of times Open vSwitch has observed the " +
			"link_state of OVS interface change.",
	},
}

>>>>>>> b47f8f274 (update)
var ovsVswitchdCoverageShowMetricsMap = map[string]*metricDetails{
	"netlink_sent": {
		help: "Number of netlink message sent to the kernel.",
	},
	"netlink_received": {
		help: "Number of netlink messages received by the kernel.",
	},
	"netlink_recv_jumbo": {
		help: "Number of netlink messages that were received from" +
			"the kernel were more than the allocated buffer.",
	},
	"netlink_overflow": {
		help: "Netlink messages dropped by the daemon due " +
			"to buffer overflow.",
	},
	"rconn_sent": {
		help: "Specifies the number of messages " +
			"that have been sent to the underlying virtual " +
			"connection (unix, tcp, or ssl) to OpenFlow devices.",
	},
	"rconn_queued": {
		help: "Specifies the number of messages that have been " +
			"queued because it couldn’t be sent using the " +
			"underlying virtual connection to OpenFlow devices.",
	},
	"rconn_discarded": {
		help: "Specifies the number of messages that " +
			"have been dropped because the send queue " +
			"had to be flushed because of reconnection.",
	},
	"rconn_overflow": {
		help: "Specifies the number of messages that have " +
			"been dropped because of the queue overflow.",
	},
	"vconn_open": {
		help: "Specifies the number of attempts to connect " +
			"to an OpenFlow Device.",
	},
	"vconn_sent": {
		help: "Specifies the number of messages sent " +
			"to the OpenFlow Device.",
	},
	"vconn_received": {
		help: "Specifies the number of messages received " +
			"from the OpenFlow Device.",
	},
	"pstream_open": {
		help: "Specifies the number of time passive connections " +
			"were opened for the remote peer to connect.",
	},
	"stream_open": {
		help: "Specifies the number of attempts to connect " +
			"to a remote peer (active connection).",
	},
	"txn_success": {
		help: "Specifies the number of times the OVSDB " +
			"transaction has successfully completed.",
	},
	"txn_error": {
		help: "Specifies the number of times the OVSDB " +
			"transaction has errored out.",
	},
	"txn_uncommitted": {
		help: "Specifies the number of times the OVSDB " +
			"transaction were uncommitted.",
	},
	"txn_unchanged": {
		help: "Specifies the number of times the OVSDB transaction " +
			"resulted in no change to the database.",
	},
	"txn_incomplete": {
		help: "Specifies the number of times the OVSDB transaction " +
			"did not complete and the client had to re-try.",
	},
	"txn_aborted": {
		help: "Specifies the number of times the OVSDB " +
			" transaction has been aborted.",
	},
	"txn_try_again": {
		help: "Specifies the number of times the OVSDB " +
			"transaction failed and the client had to re-try.",
	},
	"dpif_port_add": {
		help: "Number of times a netdev was added as a port to the dpif.",
	},
	"dpif_port_del": {
		help: "Number of times a netdev was removed from the dpif.",
	},
	"dpif_flow_flush": {
		help: "Number of times flows were flushed from the datapath " +
			"(Linux kernel datapath module).",
	},
	"dpif_flow_get": {
		help: "Number of times flows were retrieved from the " +
			"datapath (Linux kernel datapath module).",
	},
	"dpif_flow_put": {
		help: "Number of times flows were added to the datapath " +
			"(Linux kernel datapath module).",
	},
	"dpif_flow_del": {
		help: "Number of times flows were deleted from the " +
			"datapath (Linux kernel datapath module).",
	},
	"dpif_execute": {
		aggregateFrom: []string{
			"dpif_execute",
			"dpif_execute_with_help",
		},
		help: "Number of times the OpenFlow actions were executed in userspace " +
			"on behalf of the datapath.",
	},
	"bridge_reconfigure": {
		help: "Number of times OVS bridges were reconfigured.",
	},
	"xlate_actions": {
		help: "Number of times an OpenFlow actions were translated " +
			"into datapath actions.",
	},
	"xlate_actions_oversize": {
		help: "Number of times the translated OpenFlow actions into " +
			"a datapath actions were too big for a netlink attribute.",
	},
	"xlate_actions_too_many_output": {
		help: "Number of times the number of datapath actions " +
			"were more than what the kernel can handle reliably.",
	},
	"packet_in": {
		srcName: "flow_extract",
		help: "Specifies the number of times ovs-vswitchd has " +
			"handled the packet-ins on behalf of kernel datapath.",
	},
	"packet_in_drop": {
		srcName: "packet_in_overflow",
		help: "Specifies the number of times the ovs-vswitchd has dropped the " +
			"packet-ins due to resource constraints.",
	},
	"ofproto_dpif_expired": {
		help: "Number of times the flows were removed for reasons - " +
			"idle timeout, hard timeout, flow delete,  group delete, " +
			"meter delete, or eviction.",
	},
	"ofproto_flush": {
		help: "Number of times the flows from all of ofproto's " +
			"flow tables were flushed.",
	},
	"ofproto_packet_out": {
		help: "Number of times the controller injected the packet " +
			"into the kernel datapath.",
	},
	"ofproto_recv_openflow": {
		help: "Number of times an OpenFlow message was handled.",
	},
	"ofproto_reinit_ports": {
		help: "Number of times all the OpenFlow ports were reinitialized.",
	},
	"upcall_flow_limit_kill": {
		help: "Counter is increased when a number of datapath flows twice as high as current dynamic flow limit.",
	},
	"upcall_flow_limit_hit": {
		help: "Counter is increased when datapath reaches the dynamic limit of flows.",
	},
}
var registerOvsMetricsOnce sync.Once

<<<<<<< HEAD
func RegisterStandaloneOvsMetrics(ovsDBClient libovsdbclient.Client, metricsScrapeInterval int, stopChan <-chan struct{}) {
	registerOvsMetrics(ovsDBClient, metricsScrapeInterval, prometheus.DefaultRegisterer, stopChan)
}

func RegisterOvsMetricsWithOvnMetrics(ovsDBClient libovsdbclient.Client, metricsScrapeInterval int, stopChan <-chan struct{}) {
	registerOvsMetrics(ovsDBClient, metricsScrapeInterval, ovnRegistry, stopChan)
}

func registerOvsMetrics(ovsDBClient libovsdbclient.Client, metricsScrapeInterval int, registry prometheus.Registerer, stopChan <-chan struct{}) {
=======
func RegisterOvsMetrics(nodeName string, ovsDBClient libovsdbclient.Client,
	metricsScrapeInterval int, stopChan <-chan struct{}) {
	if config.Default.EnableOvsNativeMetrics {
		klog.Infof("XXXXX RegisterOvsMetrics: OVS native metrics are enabled")
		// When OVS native metrics are enabled, only need to register the additional metrics
		// which are not covered by the OVS native metrics
		RegisterAdditionalOvsMetrics()
		return
	}

	klog.Infof("XXXXX RegisterOvsMetrics: OVS native metrics is disabled")
>>>>>>> b47f8f274 (update)
	registerOvsMetricsOnce.Do(func() {
		getOvsVersionInfo(ovsDBClient)
		registry.MustRegister(prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{
				Namespace: types.MetricOvsNamespace,
				Name:      "build_info",
				Help:      "A metric with a constant '1' value labeled by ovs version.",
				ConstLabels: prometheus.Labels{
					"version": ovsVersion,
				},
			},
			func() float64 { return 1 },
		))

		// Register OVS datapath metrics.
		registry.MustRegister(metricOvsDpTotal)
		registry.MustRegister(metricOvsDp)
		registry.MustRegister(metricOvsDpIfTotal)
		registry.MustRegister(metricOvsDpFlowsTotal)
		registry.MustRegister(metricOvsDpFlowsLookupHit)
		registry.MustRegister(metricOvsDpFlowsLookupMissed)
		registry.MustRegister(metricOvsDpFlowsLookupLost)
		registry.MustRegister(metricOvsDpPacketsTotal)
		registry.MustRegister(metricOvsdpMasksHit)
		registry.MustRegister(metricOvsDpMasksTotal)
		registry.MustRegister(metricOvsDpMasksHitRatio)
		// Register OVS bridge statistics & attributes metrics
		registry.MustRegister(metricOvsBridgeTotal)
		registry.MustRegister(metricOvsBridge)
		registry.MustRegister(metricOvsBridgePortsTotal)
		registry.MustRegister(metricOvsBridgeFlowsTotal)
		// Register ovs Memory metrics
		registry.MustRegister(metricOvsHandlersTotal)
		registry.MustRegister(metricOvsRevalidatorsTotal)
		// Register OVS HW offload metrics
		registry.MustRegister(metricOvsHwOffload)
		registry.MustRegister(metricOvsTcPolicy)
		// Register OVS Interface metrics
		registry.MustRegister(metricOvsInterfaceResetsTotal)
		registry.MustRegister(metricOvsInterfaceRxDroppedTotal)
		registry.MustRegister(metricOvsInterfaceTxDroppedTotal)
		registry.MustRegister(metricOvsInterfaceRxErrorsTotal)
		registry.MustRegister(metricOvsInterfaceTxErrorsTotal)
		registry.MustRegister(metricOvsInterfaceCollisionsTotal)
		registry.MustRegister(metricOvsInterfaceTotal)
		registry.MustRegister(MetricOvsInterfaceUpWait)
		// Register the OVS coverage/show metrics
		componentCoverageShowMetricsMap[ovsVswitchd] = ovsVswitchdCoverageShowMetricsMap
		registerCoverageShowMetrics(ovsVswitchd, MetricOvsNamespace, MetricOvsSubsystemVswitchd)

		// OVS version updater
		go OvsVersionInfoUpdater(ovsDBClient, nodeName, metricsScrapeInterval, stopChan)

		// When ovnkube-node is running in privileged mode, the hostPID will be set to true,
		// and therefore it can monitor OVS running on the host using PID.
		if !config.UnprivilegedMode {
			registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{
				PidFn:     prometheus.NewPidFileFn("/var/run/openvswitch/ovs-vswitchd.pid"),
				Namespace: fmt.Sprintf("%s_%s", types.MetricOvsNamespace, types.MetricOvsSubsystemVswitchd),
			}))
			registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{
				PidFn:     prometheus.NewPidFileFn("/var/run/openvswitch/ovsdb-server.pid"),
				Namespace: fmt.Sprintf("%s_%s", types.MetricOvsNamespace, types.MetricOvsSubsystemDB),
			}))
		}

		// OVS datapath metrics updater
		go ovsDatapathMetricsUpdater(util.RunOVSAppctl, metricsScrapeInterval, stopChan)
		// OVS bridge metrics updater
		go ovsBridgeMetricsUpdater(ovsDBClient, util.RunOVSOfctl, metricsScrapeInterval, stopChan)
		// OVS interface metrics updater
		go ovsInterfaceMetricsUpdater(ovsDBClient, metricsScrapeInterval, stopChan)
		// OVS memory metrics updater
		go ovsMemoryMetricsUpdater(util.RunOvsVswitchdAppCtl, metricsScrapeInterval, stopChan)
		// OVS hw Offload metrics updater
		go ovsHwOffloadMetricsUpdater(ovsDBClient, metricsScrapeInterval, stopChan)
		// OVS coverage/show metrics updater.
		go coverageShowMetricsUpdater(ovsVswitchd, stopChan)
	})
}
