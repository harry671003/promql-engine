// Copyright (c) The Thanos Community Authors.
// Licensed under the Apache License 2.0.

package function

import (
	"context"
	"sync"
	"time"

	"github.com/thanos-io/promql-engine/execution/model"
	"github.com/thanos-io/promql-engine/execution/telemetry"
	"github.com/thanos-io/promql-engine/logicalplan"
	"github.com/thanos-io/promql-engine/query"

	"github.com/prometheus/prometheus/model/labels"
)

type absentOperator struct {
	telemetry.OperatorTelemetry

	once     sync.Once
	funcExpr *logicalplan.FunctionCall
	series   []labels.Labels
	pool     *model.VectorPool
	next     model.VectorOperator
}

func newAbsentOperator(
	funcExpr *logicalplan.FunctionCall,
	pool *model.VectorPool,
	next model.VectorOperator,
	opts *query.Options,
) *absentOperator {
	oper := &absentOperator{
		funcExpr: funcExpr,
		pool:     pool,
		next:     next,
	}
	oper.OperatorTelemetry = telemetry.NewTelemetry(oper, opts)

	return oper
}

func (o *absentOperator) String() string {
	return "[absent]"
}

func (o *absentOperator) Explain() (next []model.VectorOperator) {
	return []model.VectorOperator{o.next}
}

func (o *absentOperator) Series(_ context.Context) ([]labels.Labels, error) {
	start := time.Now()
	defer func() { o.AddSeriesExecutionTime(time.Since(start)) }()

	o.loadSeries()
	o.SetMaxSeriesCount(int64(len(o.series)))
	return o.series, nil
}

func (o *absentOperator) loadSeries() {
	// we need to put the filtered labels back for absent to compute its series properly
	o.once.Do(func() {
		o.pool.SetStepSize(1)

		// https://github.com/prometheus/prometheus/blob/df1b4da348a7c2f8c0b294ffa1f05db5f6641278/promql/functions.go#L1857
		var lm []*labels.Matcher
		switch n := o.funcExpr.Args[0].(type) {
		case *logicalplan.VectorSelector:
			lm = append(n.LabelMatchers, n.Filters...)
		case *logicalplan.MatrixSelector:
			v := n.VectorSelector
			lm = append(v.LabelMatchers, v.Filters...)
		default:
			o.series = []labels.Labels{labels.EmptyLabels()}
			return
		}

		has := make(map[string]bool)
		b := labels.NewBuilder(labels.EmptyLabels())
		for _, l := range lm {
			if l.Name == labels.MetricName {
				continue
			}
			if l.Type == labels.MatchEqual && !has[l.Name] {
				b.Set(l.Name, l.Value)
				has[l.Name] = true
			} else {
				b.Del(l.Name)
			}
		}
		o.series = []labels.Labels{b.Labels()}
	})
}

func (o *absentOperator) GetPool() *model.VectorPool {
	return o.pool
}

func (o *absentOperator) Next(ctx context.Context) ([]model.StepVector, error) {
	start := time.Now()
	defer func() { o.AddNextExecutionTime(time.Since(start)) }()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	o.loadSeries()

	vectors, err := o.next.Next(ctx)
	if err != nil {
		return nil, err
	}
	if len(vectors) == 0 {
		return nil, nil
	}

	result := o.GetPool().GetVectorBatch()
	for i := range vectors {
		sv := o.GetPool().GetStepVector(vectors[i].T)
		if len(vectors[i].Samples) == 0 && len(vectors[i].Histograms) == 0 {
			sv.AppendSample(o.GetPool(), 0, 1)
		}
		result = append(result, sv)
		o.next.GetPool().PutStepVector(vectors[i])
	}
	o.next.GetPool().PutVectors(vectors)
	return result, nil
}
