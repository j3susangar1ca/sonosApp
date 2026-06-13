package telemetry

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// Gauges
	QueueSizeFunc   func() float64
	ActiveUsersFunc func() float64

	// Counters
	playCount = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "jukebox_play_count_total",
		Help: "Total number of tracks played successfully.",
	})
	skipCount = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "jukebox_skip_count_total",
		Help: "Total number of democratic skips triggered.",
	})
	cbFailures = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "jukebox_cb_f_total",
		Help: "Total number of circuit breaker failures registered.",
	})

	// Histograms
	youtubeLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "jukebox_youtube_resolution_latency_seconds",
		Help:    "Latency of YouTube stream resolution tasks in seconds.",
		Buckets: prometheus.DefBuckets,
	})
	sonosLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "jukebox_sonos_soap_latency_seconds",
		Help:    "Latency of Sonos SOAP responses in seconds.",
		Buckets: prometheus.DefBuckets,
	})
)

func init() {
	prometheus.MustRegister(playCount)
	prometheus.MustRegister(skipCount)
	prometheus.MustRegister(cbFailures)
	prometheus.MustRegister(youtubeLatency)
	prometheus.MustRegister(sonosLatency)
}

// RegisterGaugeFuncs registers the dynamic gauge functions.
// Must be called after assigning QueueSizeFunc and ActiveUsersFunc.
func RegisterGaugeFuncs() {
	if QueueSizeFunc != nil {
		prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "jukebox_queue_size",
			Help: "Current size of the Jukebox play queue.",
		}, QueueSizeFunc))
	}
	if ActiveUsersFunc != nil {
		prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "jukebox_active_users",
			Help: "Current number of connected active users.",
		}, ActiveUsersFunc))
	}
}

// IncrementPlayCount increments the play count counter.
func IncrementPlayCount() {
	playCount.Inc()
}

// IncrementSkipCount increments the skip count counter.
func IncrementSkipCount() {
	skipCount.Inc()
}

// IncrementCircuitBreakerFailures increments the circuit breaker failure counter.
func IncrementCircuitBreakerFailures() {
	cbFailures.Inc()
}

// ObserveYoutubeLatency records YouTube resolution latency in seconds.
func ObserveYoutubeLatency(duration float64) {
	youtubeLatency.Observe(duration)
}

// ObserveSonosLatency records Sonos SOAP action response latency in seconds.
func ObserveSonosLatency(duration float64) {
	sonosLatency.Observe(duration)
}

// Handler returns the HTTP handler for the /metrics endpoint.
func Handler() http.Handler {
	return promhttp.Handler()
}
