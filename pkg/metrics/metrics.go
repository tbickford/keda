package metrics

// Server an HTTP serving instance to track metrics
type Server interface {
	StartServer(address string, pattern string)
	RecordHPAScalerErrorTotals(namespace string, metric string, selector string, err error)
	RecordHPAScalerMetrics(namespace string, metric string, selector string, value int64)
}
