package main

import (
	"context"
	"fmt"
	"time"

	_ "github.com/influxdata/influxdb1-client" // this is important because of the bug in go mod
	client "github.com/influxdata/influxdb1-client/v2"

	"github.com/prometheus/client_golang/prometheus"
	promodel "github.com/prometheus/client_model/go"
	"github.com/testground/sdk-go/runtime"
)

// InfluxBridge takes a set of metrics and periodically writes them
// to an influx store
type InfluxBridge struct {
	interval time.Duration

	g      prometheus.Gatherer
	labels prometheus.Labels
	sink   client.Client
}

// Start begins periodically gathering metrics and writing them to the sync
func (j *InfluxBridge) Start(ctx context.Context, re *runtime.RunEnv) {
	go j.background(ctx, re)
}

func (j *InfluxBridge) background(ctx context.Context, re *runtime.RunEnv) {
	t := time.NewTimer(j.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			j.Save(re)
			return
		case <-t.C:
			j.Save(re)
			t.Reset(j.interval)
		}
	}
}

// Save records one snapshot of gathered metrics to the sink.
func (j *InfluxBridge) Save(re *runtime.RunEnv) {
	metrics, err := j.g.Gather()
	if err != nil {
		re.RecordFailure(err)
		return
	}
	re.RecordMessage("Recording metrics snapshot.")
	batch, err := client.NewBatchPoints(client.BatchPointsConfig{
		Database: "testground",
	})
	if err != nil {
		re.RecordFailure(err)
		return
	}

	for _, f := range metrics {
		p := j.tag(f, j.labels)
		batch.AddPoint(p)
	}

	j.sink.Write(batch)
}

func (j *InfluxBridge) tag(family *promodel.MetricFamily, l prometheus.Labels) *client.Point {
	name := *family.Name
	tags := make(map[string]string)
	for k, v := range l {
		tags[k] = v
	}
	fields := make(map[string]interface{})

	// labels which are constant within the family become tags
	// keys which vary are in fieldTags, and are used to create field name.
	fieldTags := make(map[string]bool)
	ct := make(map[string]string)
	for _, m := range family.GetMetric() {
		for _, l := range m.GetLabel() {
			if _, ok := ct[l.GetName()]; ok {
				if ct[l.GetName()] != l.GetValue() {
					fieldTags[l.GetName()] = true
				}
			} else {
				ct[l.GetName()] = l.GetValue()
			}
		}
	}
	for k, v := range ct {
		if _, ok := fieldTags[k]; !ok {
			tags[k] = v
		}
	}

	switch family.GetType() {
	case promodel.MetricType_COUNTER:
		for _, m := range family.GetMetric() {
			n := makeName(fieldTags, m.GetLabel())
			fields[n] = m.GetCounter().GetValue()
		}
	case promodel.MetricType_GAUGE:
		for _, m := range family.GetMetric() {
			n := makeName(fieldTags, m.GetLabel())
			fields[n] = m.GetGauge().GetValue()
		}
	case promodel.MetricType_HISTOGRAM:
		for _, m := range family.GetMetric() {
			buckets := m.GetHistogram().GetBucket()
			for _, b := range buckets {
				fields[fmt.Sprintf("%f", b.GetUpperBound())] = b.GetCumulativeCount()
			}
			n := makeName(fieldTags, m.GetLabel())
			fields[n] = m.GetHistogram().GetSampleSum()
		}
	case promodel.MetricType_SUMMARY:
		for _, m := range family.GetMetric() {
			quantile := m.GetSummary().GetQuantile()
			for _, q := range quantile {
				fields[fmt.Sprintf("%f", q.GetQuantile())] = q.GetValue()
			}
			n := makeName(fieldTags, m.GetLabel())
			fields[n] = m.GetSummary().GetSampleSum()
		}
	case promodel.MetricType_UNTYPED:
		for _, m := range family.GetMetric() {
			n := makeName(fieldTags, m.GetLabel())
			fields[n] = m.GetUntyped().GetValue()
		}
	}

	p, _ := client.NewPoint(name, tags, fields, time.Now())
	return p
}

func makeName(tags map[string]bool, label []*promodel.LabelPair) string {
	s := ""
	for _, l := range label {
		if tags[l.GetName()] {
			if s != "" {
				s += ","
			}
			s += l.GetName() + "_" + l.GetValue()
		}
	}
	if s == "" {
		s = "value"
	}
	return s
}

// NewInfluxBridge careates an influx bridge
func NewInfluxBridge(metrics prometheus.Gatherer, interval time.Duration, destination client.Client) *InfluxBridge {
	bridge := InfluxBridge{
		interval: interval,
		g:        metrics,
		labels:   prometheus.Labels{},
		sink:     destination,
	}
	return &bridge
}

// SavePrometheusAsDiagnostics drops a gatherer into a raw asset for the run env to upload.
func SavePrometheusAsDiagnostics(re *runtime.RunEnv, gatherer prometheus.Gatherer) {
	c, err := runtime.NewInfluxDBClient(re)
	if err != nil {
		panic(err)
	}
	extras := prometheus.Labels{
		"plan":     re.TestPlan,
		"case":     re.TestCase,
		"run":      re.TestRun,
		"group_id": re.TestGroupID,
	}
	bridge := NewInfluxBridge(gatherer, time.Second, c)
	bridge.labels = extras
	bridge.Start(context.Background(), re)
}
