// Copyright 2022 The Cockroach Authors.
//
// Use of this software is governed by the CockroachDB Software License
// included in the /LICENSE file.

package server

import (
	"context"
	"net/http"

	"github.com/cockroachdb/cockroach/pkg/jobs"
	"github.com/cockroachdb/cockroach/pkg/server/status"
	"github.com/cockroachdb/cockroach/pkg/server/telemetry"
	"github.com/cockroachdb/cockroach/pkg/sql"
	"github.com/cockroachdb/cockroach/pkg/sql/pgwire"
	"github.com/cockroachdb/cockroach/pkg/util/log"
	"github.com/cockroachdb/cockroach/pkg/util/metric"
	"github.com/cockroachdb/cockroach/pkg/util/timeutil"
	"github.com/cockroachdb/errors"
	"github.com/prometheus/common/expfmt"
)

var (
	// Counter to count accesses to the prometheus vars endpoint /_status/vars .
	telemetryPrometheusLoadVars = telemetry.GetCounterOnce("monitoring.prometheus.load_vars")
)

type loadEndpoint struct {
	// Capture the start time of the process as well as the initial User and
	// System CPU times so all reporting can be done relative to these.
	initTimeNanos      int64
	initUserTimeMillis int64
	initSysTimeMillis  int64

	cpuUserNanos  *metric.Gauge
	cpuSysNanos   *metric.Gauge
	cpuNowNanos   *metric.Gauge
	uptimeSeconds *metric.GaugeFloat64

	registry     *metric.Registry
	exporterLoad metric.PrometheusExporter
	exporterVars metric.PrometheusExporter

	mainMetricSource metricMarshaler
}

// additionalLoadEndpointMetricsSet returns the set of all custom metric names that
// the load endpoint should export.
func additionalLoadEndpointMetricsSet() map[string]struct{} {
	return map[string]struct{}{
		jobs.MetaRunningNonIdleJobs.Name: {},
		pgwire.MetaConns.Name:            {},
		pgwire.MetaNewConns.Name:         {},
		sql.MetaQueryExecuted.Name:       {},
	}
}

func newLoadEndpoint(
	rsr *status.RuntimeStatSampler, mainMetricSource metricMarshaler,
) (*loadEndpoint, error) {
	initUserTimeMillis, initSysTimeMillis, err := status.GetProcCPUTime(context.Background())
	if err != nil {
		return nil, errors.Wrap(err, "unable to get proc cpu time")
	}
	result := &loadEndpoint{
		initTimeNanos:      timeutil.Now().UnixNano(),
		initUserTimeMillis: initUserTimeMillis,
		initSysTimeMillis:  initSysTimeMillis,
		registry:           metric.NewRegistry(),
		exporterLoad:       metric.MakePrometheusExporter(),
		mainMetricSource:   mainMetricSource,
	}
	// Exporter for the selected metrics that also show in /_status/vars.
	result.exporterVars = metric.MakePrometheusExporterForSelectedMetrics(additionalLoadEndpointMetricsSet())

	result.cpuUserNanos = metric.NewGauge(rsr.CPUUserNS.GetMetadata())
	result.cpuSysNanos = metric.NewGauge(rsr.CPUSysNS.GetMetadata())
	result.cpuNowNanos = metric.NewGauge(rsr.CPUNowNS.GetMetadata())
	result.uptimeSeconds = metric.NewGaugeFloat64(rsr.Uptime.GetMetadata())

	result.registry.AddMetric(result.cpuUserNanos)
	result.registry.AddMetric(result.cpuSysNanos)
	result.registry.AddMetric(result.cpuNowNanos)
	result.registry.AddMetric(result.uptimeSeconds)

	return result, nil
}

// Exporter for the load vars that are provided only by the load handler.
func (le *loadEndpoint) scrapeLoadVarsIntoPrometheus(pm *metric.PrometheusExporter) {
	pm.ScrapeRegistry(le.registry, metric.WithIncludeChildMetrics(true), metric.WithIncludeAggregateMetrics(true))
}

// Handler responsible for serving the instant values of selected
// load metrics. These include user and system CPU time currently.
// TODO(knz): this should probably include memory usage too somehow.
func (le *loadEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	telemetry.Inc(telemetryPrometheusLoadVars)

	userTimeMillis, sysTimeMillis, err := status.GetProcCPUTime(ctx)
	if err != nil {
		// Just log but don't return an error to match the _status/vars metrics handler.
		log.Ops.Errorf(ctx, "unable to get cpu usage: %v", err)
	}

	// The CPU metrics are updated on each call.
	// cpuTime.{User,Sys} are in milliseconds, convert to nanoseconds.
	utime := (userTimeMillis - le.initUserTimeMillis) * 1e6
	stime := (sysTimeMillis - le.initSysTimeMillis) * 1e6
	le.cpuUserNanos.Update(utime)
	le.cpuSysNanos.Update(stime)
	le.cpuNowNanos.Update(timeutil.Now().UnixNano())
	le.uptimeSeconds.Update(float64(timeutil.Now().UnixNano()-le.initTimeNanos) / 1e9)

	contentType := expfmt.Negotiate(r.Header)

	if err := le.exporterLoad.ScrapeAndPrintAsText(w, contentType, le.scrapeLoadVarsIntoPrometheus); err != nil {
		log.Errorf(r.Context(), "%v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := le.exporterVars.ScrapeAndPrintAsText(w, contentType, le.mainMetricSource.ScrapeIntoPrometheus); err != nil {
		log.Errorf(r.Context(), "%v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
