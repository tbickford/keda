package metrics

import (
	"log"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	metricLabels = []string{"namespace", "metric", "selector"}
	scalerErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "keda_hpa",
			Subsystem: "scaler",
			Name:      "errors",
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
	errorTotals = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "keda_hpa",
			Subsystem: "scaler",
			Name:      "error_totals",
			Help:      "Total number of scaler errors",
		},
		[]string{},
	)
)

type prometheusMetricsExporter struct {
	registry *prometheus.Registry
}

// NewPrometheusMetricsExporter return a new instance of the prometheus metrics server
func NewPrometheusMetricsExporter() Exporter {
	registry := prometheus.NewRegistry()
	registry.MustRegister(scalerErrors)
	registry.MustRegister(scalerMetricsValue)
	registry.MustRegister(errorTotals)
	errorTotals.With(prometheus.Labels{}) // initialize errors at 0
	return &prometheusMetricsExporter{registry: registry}
}

// StartServer starts an HTTP server to serve the metrics to export
func (exporter prometheusMetricsExporter) StartServer(address string, pattern string) {
	log.Printf("Starting metrics server at %v", address)
	http.Handle(pattern, promhttp.HandlerFor(exporter.registry, promhttp.HandlerOpts{}))
	log.Fatal(http.ListenAndServe(address, nil))
}

// RecordHPAScalerMetrics create a measurement of the external metric used by the HPA
func (exporter prometheusMetricsExporter) RecordHPAScalerMetrics(namespace string, metric string, selector string, value int64) {
	scalerMetricsValue.With(getLabels(namespace, metric, selector)).Set(float64(value))
}

// RecordHPAScalerErrors counts the number of errors occurred in trying get an external metric used by the HPA
func (exporter prometheusMetricsExporter) RecordHPAScalerErrors(namespace string, metric string, selector string, err error) {
	if err != nil {
		scalerErrors.With(getLabels(namespace, metric, selector)).Inc()
		errorTotals.With(prometheus.Labels{}).Inc()
		return
	}
	// initialize metric with 0 if not already set
	scalerErrors.GetMetricWith(getLabels(namespace, metric, selector))
}

func getLabels(namespace string, metric string, selector string) prometheus.Labels {
	return prometheus.Labels{"namespace": namespace, "metric": metric, "selector": selector}
}
