package metrics

import (
	"log"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	metricLabels      = []string{"namespace", "metric", "selector"}
	scalerErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "keda_hpa",
			Subsystem: "scaler",
			Name:      "errors_total",
			Help:      "Number of scaler errors",
		},
		metricLabels,
	)
	scalerMetricsValue = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "keda_hpa",
			Subsystem: "scaler",
			Name:      "metrics_value",
			Help:      "Metric Value",
		},
		metricLabels,
	)
)

// PrometheusMetricServer the type of MetricsServer
type PrometheusMetricServer struct{}

var registry *prometheus.Registry

func init() {
	registry = prometheus.NewRegistry()
	registry.MustRegister(scalerErrorsTotal)
	registry.MustRegister(scalerMetricsValue)
}

// NewServer creates a new http serving instance of prometheus metrics
func (metricsServer PrometheusMetricServer) NewServer(address string, pattern string) {
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	log.Printf("Starting metrics server at %v", address)
	http.Handle(pattern, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	log.Fatal(http.ListenAndServe(address, nil))
}

// RecordHPAScalerMetrics create a measurement of the external metric used by the HPA
func (metricsServer PrometheusMetricServer) RecordHPAScalerMetrics(namespace string, metric string, selector string, value int64) {
	scalerMetricsValue.With(getLabels(namespace, metric, selector)).Set(float64(value))
}

// RecordHPAScalerErrorTotals counts the number of errors occurred in trying get an external metric used by the HPA
func (metricsServer PrometheusMetricServer) RecordHPAScalerErrorTotals(namespace string, metric string, selector string, err error) {
	if err != nil {
		scalerErrorsTotal.With(getLabels(namespace, metric, selector)).Inc()
		return
	}
	// initialize metric with 0 if not already set
	scalerErrorsTotal.GetMetricWith(getLabels(namespace, metric, selector))
}

func getLabels(namespace string, metric string, selector string) prometheus.Labels {
	return prometheus.Labels{"namespace": namespace, "metric": metric, "selector": selector}
}
