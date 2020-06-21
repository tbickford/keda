package metrics

// Server an HTTP serving instance to track metrics
type Server interface {
	NewServer(address string, pattern string)
	RecordScalerErrorTotals(namespace string, metric string, selector string, err error)
	RecordScalerMetrics(namespace string, metric string, selector string, value int64)
}
