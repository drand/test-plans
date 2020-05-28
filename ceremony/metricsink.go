package main

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/testground/sdk-go/runtime"
)

// TODO: JSON bridge can live in drand/metrics

// JSONBridge takes a set of metrics and periodically writes them
// to a json sink.
type JSONBridge struct {
	interval time.Duration

	g      prometheus.Gatherer
	labels prometheus.Labels
	sink   *json.Encoder
}

// Start begins periodically gathering metrics and writing them to the sync
func (j *JSONBridge) Start(ctx context.Context) {
	go j.background(ctx)
}

func (j *JSONBridge) background(ctx context.Context) {
	t := time.NewTimer(j.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			j.Save()
			t.Reset(j.interval)
		}
	}
}

// Save records one snapshot of gathered metrics to the sink.
func (j *JSONBridge) Save() {
	metrics, err := j.g.Gather()
	if err != nil {
		return
	}
	vec, err := expfmt.ExtractSamples(&expfmt.DecodeOptions{
		Timestamp: model.Now(),
	}, metrics...)
	for _, s := range vec {
		j.tag(s.Metric, j.labels)
		j.sink.Encode(s)
	}
}

func (j *JSONBridge) tag(m model.Metric, l prometheus.Labels) {
	for n, v := range l {
		m[model.LabelName(n)] = model.LabelValue(v)
	}
}

// NewJSONBridge careates a JSON bridge
func NewJSONBridge(metrics prometheus.Gatherer, interval time.Duration, destination io.Writer) *JSONBridge {
	enc := json.NewEncoder(destination)
	bridge := JSONBridge{
		interval: interval,
		g:        metrics,
		labels:   prometheus.Labels{},
		sink:     enc,
	}
	return &bridge
}

func SavePrometheusAsDiagnostics(re *runtime.RunEnv, gatherer prometheus.Gatherer, filename string) {
	f, err := re.CreateRawAsset(filename)
	if err != nil {
		panic(err)
	}
	extras := prometheus.Labels{
		"plan":     re.TestPlan,
		"case":     re.TestCase,
		"run":      re.TestRun,
		"group_id": re.TestGroupID,
	}
	bridge := NewJSONBridge(gatherer, time.Second, f)
	bridge.labels = extras
	bridge.Start(context.Background())
}
