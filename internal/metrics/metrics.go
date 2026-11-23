// Package metrics defines the Prometheus metrics of the synchroniser. Labels
// are limited to low-cardinality values; no message data ever appears.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	SessionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gmailsync_sessions_total", Help: "Finished synchronisation sessions."}, []string{"type", "status"})
	MessagesDiscovered = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gmailsync_messages_discovered_total", Help: "Messages found to process."})
	MessagesCopied = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gmailsync_messages_copied_total", Help: "Messages copied to at least one folder."})
	MessagesSkipped = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gmailsync_messages_skipped_total", Help: "Messages already present or excluded."})
	MessagesFailed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gmailsync_messages_failed_total", Help: "Messages that failed and were recorded for retry."})
	MessagesRecovered = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gmailsync_messages_recovered_total", Help: "Failed messages that were delivered on retry."})
	BytesCopied = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gmailsync_bytes_copied_total", Help: "Bytes appended to the destination."})
	SessionDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name: "gmailsync_session_duration_seconds", Help: "Duration of finished sessions.",
		Buckets: prometheus.ExponentialBuckets(1, 3, 10)})
	SyncActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "gmailsync_sync_active", Help: "Sessions running in this process."})
	QueueDepth = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "gmailsync_queue_depth", Help: "Sessions waiting to run."})
	PendingFailures = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "gmailsync_pending_failures", Help: "Failed messages awaiting retry."})
	NeedsReauth = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "gmailsync_oauth_needs_reauth", Help: "Accounts whose Google authorisation must be renewed."})
	LastSuccess = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gmailsync_last_success_timestamp_seconds", Help: "Unix time of the last successful session."}, []string{"account"})
)
