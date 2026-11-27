// Package metrics exposes Prometheus metrics on an internal listener.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	Registry = prometheus.NewRegistry()

	HTTPRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hedvi_http_requests_total", Help: "HTTP requests by route and status class."}, []string{"route", "status"})
	HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "hedvi_http_request_duration_seconds", Help: "HTTP request latency by route.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5}}, []string{"route"})
	Accepted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hedvi_emails_accepted_total", Help: "Durably accepted emails by mode."}, []string{"mode"})
	SendOutcomes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hedvi_send_outcomes_total", Help: "Provider send outcomes."}, []string{"outcome"})
	OutboxPublished = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hedvi_outbox_published_total", Help: "Outbox jobs published by topic."}, []string{"topic"})
	EventsApplied = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hedvi_provider_events_total", Help: "Provider events by type and result."}, []string{"type", "result"})
	WebhookDeliveries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hedvi_webhook_deliveries_total", Help: "Webhook delivery attempts by result."}, []string{"result"})

	// Gauges refreshed by the monitor loop from PostgreSQL.
	OldestQueuedSeconds = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "hedvi_oldest_queued_email_seconds", Help: "Age of the oldest live email waiting to be sent."})
	OutboxBacklog = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hedvi_outbox_backlog", Help: "Unpublished outbox jobs that are due."}, []string{"topic"})
	UnknownAttempts = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "hedvi_unknown_send_attempts", Help: "Send attempts with unknown outcome awaiting reconciliation."})
	OpenAlerts = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hedvi_open_alerts", Help: "Open alerts by severity."}, []string{"severity"})
)

func init() {
	Registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		HTTPRequests, HTTPDuration, Accepted, SendOutcomes, OutboxPublished, EventsApplied, WebhookDeliveries,
		OldestQueuedSeconds, OutboxBacklog, UnknownAttempts, OpenAlerts)
}

func Handler() http.Handler { return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{}) }
