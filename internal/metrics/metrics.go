// Package metrics holds the stacker's Prometheus metrics.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const ns = "astro_stacker"

var (
	LightsPending = promauto.NewGauge(prometheus.GaugeOpts{Namespace: ns, Name: "lights_pending",
		Help: "Lights waiting to be stacked or due for a retry."})
	LightsDone = promauto.NewGauge(prometheus.GaugeOpts{Namespace: ns, Name: "lights_done",
		Help: "Lights decided: added to a master or left out for a reason."})
	SubsDead = promauto.NewGauge(prometheus.GaugeOpts{Namespace: ns, Name: "subs_dead",
		Help: "Subs that failed MaxAttempts times and are left out until reset."})
	PreviewsPending = promauto.NewGauge(prometheus.GaugeOpts{Namespace: ns, Name: "previews_pending",
		Help: "Lights still to get a preview."})
	WorkersBusy = promauto.NewGauge(prometheus.GaugeOpts{Namespace: ns, Name: "workers_busy",
		Help: "Targets being stacked right now."})

	Subs = promauto.NewCounterVec(prometheus.CounterOpts{Namespace: ns, Name: "subs_total",
		Help: "Subs recorded, by outcome (added, low_score, calibration, registration, failed, dead, ...)."}, []string{"status"})
	Batches = promauto.NewCounterVec(prometheus.CounterOpts{Namespace: ns, Name: "batches_total",
		Help: "Stacking batches, by result."}, []string{"result"})
	BatchSeconds = promauto.NewHistogram(prometheus.HistogramOpts{Namespace: ns, Name: "batch_seconds",
		Help: "Time to calibrate, register and stack one batch.", Buckets: prometheus.ExponentialBuckets(5, 2, 9)})
	MastersUpdated = promauto.NewCounter(prometheus.CounterOpts{Namespace: ns, Name: "masters_updated_total",
		Help: "Masters published."})
	Mosaics = promauto.NewCounterVec(prometheus.CounterOpts{Namespace: ns, Name: "mosaics_total",
		Help: "Mosaic builds, by result."}, []string{"result"})
	MosaicSeconds = promauto.NewHistogram(prometheus.HistogramOpts{Namespace: ns, Name: "mosaic_seconds",
		Help: "Time to build one mosaic.", Buckets: prometheus.ExponentialBuckets(10, 2, 9)})
	Covers = promauto.NewCounterVec(prometheus.CounterOpts{Namespace: ns, Name: "covers_total",
		Help: "Colour cover renders, by result (colour, mono, failed)."}, []string{"result"})
	SirilSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{Namespace: ns, Name: "siril_seconds",
		Help: "Siril script run time, by its main command.", Buckets: prometheus.ExponentialBuckets(1, 2, 11)},
		[]string{"command", "result"})
	PreviewsRendered = promauto.NewCounterVec(prometheus.CounterOpts{Namespace: ns, Name: "previews_rendered_total",
		Help: "Sub previews rendered, by result."}, []string{"result"})
	FramesIndexed = promauto.NewCounter(prometheus.CounterOpts{Namespace: ns, Name: "frames_indexed_total",
		Help: "Frames whose headers were indexed."})
	EventListeners = promauto.NewGauge(prometheus.GaugeOpts{Namespace: ns, Name: "event_listeners",
		Help: "Clients following the event stream."})
	TSVerdicts = promauto.NewGaugeVec(prometheus.GaugeOpts{Namespace: ns, Name: "ts_verdicts",
		Help: "Verdicts sent to Target Scheduler, by verdict (reject, accept) and state (sent, applied, moot, overridden)."},
		[]string{"verdict", "state"})
	TSVerdictsSent = promauto.NewCounterVec(prometheus.CounterOpts{Namespace: ns, Name: "ts_verdicts_sent_total",
		Help: "Verdicts written for Target Scheduler, by kind (new, retry, undo, redo)."}, []string{"kind"})
)
