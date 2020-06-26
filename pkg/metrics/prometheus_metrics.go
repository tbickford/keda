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

type prometheusMetricsExporter struct {
	registry *prometheus.Registry
}

// NewPrometheusMetricsExporter return a new instance of the prometheus metrics server
func NewPrometheusMetricsExporter() Exporter {
	registry := prometheus.NewRegistry()
	registry.MustRegister(scalerErrorsTotal)
	registry.MustRegister(scalerMetricsValue)
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

// RecordHPAScalerErrorTotals counts the number of errors occurred in trying get an external metric used by the HPA
func (exporter prometheusMetricsExporter) RecordHPAScalerErrorTotals(namespace string, metric string, selector string, err error) {
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
