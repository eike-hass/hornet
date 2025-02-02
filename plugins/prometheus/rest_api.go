package prometheus

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/iotaledger/hive.go/events"
)

var (
	restapiHTTPErrorCount prometheus.Gauge

	restapiPoWCompletedCount prometheus.Gauge
	restapiPoWBlockSizes     prometheus.Histogram
	restapiPoWDurations      prometheus.Histogram
)

func configureRestAPI() {
	restapiHTTPErrorCount = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "iota",
			Subsystem: "restapi",
			Name:      "http_request_error_count",
			Help:      "The amount of encountered HTTP request errors.",
		},
	)

	restapiPoWCompletedCount = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "iota",
			Subsystem: "restapi",
			Name:      "pow_count",
			Help:      "The amount of completed REST API PoW requests.",
		},
	)

	restapiPoWBlockSizes = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: "iota",
			Subsystem: "restapi",
			Name:      "pow_block_sizes",
			Help:      "The block size of REST API PoW requests.",
			Buckets:   powBlockSizeBuckets,
		})

	restapiPoWDurations = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: "iota",
			Subsystem: "restapi",
			Name:      "pow_durations",
			Help:      "The duration of REST API PoW requests [s].",
			Buckets:   powDurationBuckets,
		})

	registry.MustRegister(restapiHTTPErrorCount)

	registry.MustRegister(restapiPoWCompletedCount)
	registry.MustRegister(restapiPoWBlockSizes)
	registry.MustRegister(restapiPoWDurations)

	deps.RestAPIMetrics.Events.PoWCompleted.Attach(events.NewClosure(func(blockSize int, duration time.Duration) {
		restapiPoWBlockSizes.Observe(float64(blockSize))
		restapiPoWDurations.Observe(duration.Seconds())
	}))

	addCollect(collectRestAPI)
}

func collectRestAPI() {
	restapiHTTPErrorCount.Set(float64(deps.RestAPIMetrics.HTTPRequestErrorCounter.Load()))
	restapiPoWCompletedCount.Set(float64(deps.RestAPIMetrics.PoWCompletedCounter.Load()))
}
