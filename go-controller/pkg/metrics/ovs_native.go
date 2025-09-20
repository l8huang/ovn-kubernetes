package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sync"

	libovsdbclient "github.com/ovn-kubernetes/libovsdb/client"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/common/expfmt"
	"k8s.io/klog/v2"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
)

var (
	RunOvsVswitchdAppCtlMetricsShow = util.RunOvsVswitchdAppCtlMetricsShow
)

type ovsNativeMetricsHandler struct {
	ovsDBClient libovsdbclient.Client
	nodeName    string
}

const FmtText = `text/plain; version=` + expfmt.TextVersion + `; charset=utf-8`

func writeRegisteredMetrics(registry prometheus.Gatherer, w io.Writer) error {
	mfs, err := registry.Gather()
	if err != nil {
		return err
	}
	enc := expfmt.NewEncoder(w, FmtText)
	for _, mf := range mfs {
		if err := enc.Encode(mf); err != nil {
			return err
		}
	}
	return nil

}

func (h *ovsNativeMetricsHandler) handleMetircsRequest(w http.ResponseWriter, r *http.Request) {

	// log the request headers
	klog.Infof("XXXXX handleMetircsRequest: %v", r.Header)
	w.Header().Set("Content-Type", string(FmtText))

	h.updateNonNativeMetrics()
	// write out the registered metrics
	if err := writeRegisteredMetrics(prometheus.DefaultGatherer, io.Writer(w)); err != nil {
		klog.Errorf("Failed to write registered metrics: %v", err)
		return
	}

	// write out the ovs metrics
	if config.Default.EnableOvsNativeMetrics {
		ovsMetrics, err := collectOvsNativeMetrics(io.Writer(w))
		if err != nil {
			klog.Errorf("Failed to collect ovs metrics: %v", err)
			return
		}
		_, err = io.Copy(w, ovsMetrics)
		if err != nil {
			klog.Errorf("Failed to write ovs metrics: %v", err)
			return
		}
	}
}

func (h *ovsNativeMetricsHandler) updateNonNativeMetrics() {

	// OVS version updater
	if err := getOvsVersionInfo(h.nodeName, h.ovsDBClient); err != nil {
		klog.Errorf("Error getting ovs version: %v", err)
	}

	// OVS datapath metrics updater
	updateOvsDatapathMetrics(util.RunOvsVswitchdAppCtl)

	resetOvsBridgeMetrics()
	// set geneve interface metrics
	err := geneveInterfaceMetricsUpdate()
	if err != nil {
		klog.Errorf("%s", err.Error())
	}
	// update ovs bridge metrics
	if err = updateOvsBridgeMetrics(h.ovsDBClient, util.RunOVSOfctl); err != nil {
		klog.Errorf("Getting ovs bridge info failed: %s", err.Error())
	}

	// OVS memory metrics updater
	if err := setOvsMemoryMetrics(util.RunOvsVswitchdAppCtl); err != nil {
		klog.Errorf("Setting ovs memory metrics failed: %s", err.Error())
	}

	// OVS hw Offload metrics updater
	if err := setOvsHwOffloadMetrics(h.ovsDBClient); err != nil {
		klog.Errorf("Setting ovs hardware offload metrics failed: %s", err.Error())
	}

	// OVS coverage/show metrics updater.
	setCoverageShowMetric(ovsVswitchd)
}

func collectOvsNativeMetrics(w io.Writer) (io.ReadCloser, error) {
	// TODO: the size of the output of metrics/show could be too large, consider
	// using a pipe to read the output and running metrics/show non-blocking.
	stdout, stderr, err := util.RunOvsVswitchdAppCtlMetricsShow()
	if err != nil {
		return nil, fmt.Errorf("failed to run metrics/show: error %v; stderr: %s", err, stderr)
	}

	return io.NopCloser(stdout), nil
}

func registerOvsInterfaceExtraMetrics(metricNamespace, metricSubsystem string) {
	for InterfaceMetricName, InterfaceMetricInfo := range ovsInterfaceExtraMetricsDataMap {
		InterfaceMetricInfo.metric = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: metricSubsystem,
			Name:      InterfaceMetricName,
			Help:      InterfaceMetricInfo.help,
		},
			[]string{
				"bridge",
				"port",
				"name",
			})
		prometheus.MustRegister(InterfaceMetricInfo.metric)
	}
	ovsInterfaceMetricsDataMap = ovsInterfaceExtraMetricsDataMap
}

var registerOvsNativeMetricsOnce sync.Once

func RegisterAdditionalOvsMetrics() {
	registerOvsNativeMetricsOnce.Do(func() {
		prometheus.MustRegister(metricOvsVersion)

		// Register OVS datapath metrics.
		prometheus.MustRegister(metricOvsDpTotal)
		prometheus.MustRegister(metricOvsDp)
		prometheus.MustRegister(metricOvsDpIfTotal)
		prometheus.MustRegister(metricOvsDpIf)
		prometheus.MustRegister(metricOvsDpMasksHitRatio)
		prometheus.MustRegister(metricOvsDpOffloadedFlowsTotal)

		// Register OVS HW offload metrics
		prometheus.MustRegister(metricOvsHwOffload)
		prometheus.MustRegister(metricOvsTcPolicy)
		// Register OVS Interface metrics
		registerOvsInterfaceExtraMetrics(MetricOvsNamespace, MetricOvsSubsystemVswitchd)

		prometheus.MustRegister(MetricOvsInterfaceUpWait)
		// Register the OVS coverage/show metrics
		componentCoverageShowMetricsMap[ovsVswitchd] = ovsVswitchdCoverageShowMetricsMap
		registerCoverageShowMetrics(ovsVswitchd, MetricOvsNamespace, MetricOvsSubsystemVswitchd)

		// When ovnkube-node is running in privileged mode, the hostPID will be set to true,
		// and therefore it can monitor OVS running on the host using PID.
		if !config.UnprivilegedMode {
			prometheus.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{
				PidFn:     prometheus.NewPidFileFn("/var/run/openvswitch/ovs-vswitchd.pid"),
				Namespace: fmt.Sprintf("%s_%s", MetricOvsNamespace, MetricOvsSubsystemVswitchd),
			}))
			prometheus.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{
				PidFn:     prometheus.NewPidFileFn("/var/run/openvswitch/ovsdb-server.pid"),
				Namespace: fmt.Sprintf("%s_%s", MetricOvsNamespace, MetricOvsSubsystemOvsDB),
			}))
		}

	})
}
