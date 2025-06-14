// Copyright (c) The Thanos Community Authors.
// Licensed under the Apache License 2.0.

package exchange

import (
	"context"
	"sync"
	"time"

	"github.com/thanos-io/promql-engine/execution/model"
	"github.com/thanos-io/promql-engine/execution/telemetry"
	"github.com/thanos-io/promql-engine/extlabels"
	"github.com/thanos-io/promql-engine/query"

	"github.com/prometheus/prometheus/model/labels"
)

type pair struct{ a, b int }

type duplicateLabelCheckOperator struct {
	telemetry.OperatorTelemetry

	once sync.Once
	next model.VectorOperator

	p []pair
	c []uint64
}

func NewDuplicateLabelCheck(next model.VectorOperator, opts *query.Options) model.VectorOperator {
	oper := &duplicateLabelCheckOperator{
		next: next,
	}
	oper.OperatorTelemetry = telemetry.NewTelemetry(oper, opts)

	return oper
}

func (d *duplicateLabelCheckOperator) Next(ctx context.Context) ([]model.StepVector, error) {
	start := time.Now()
	defer func() { d.AddNextExecutionTime(time.Since(start)) }()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	if err := d.init(ctx); err != nil {
		return nil, err
	}

	in, err := d.next.Next(ctx)
	if err != nil {
		return nil, err
	}
	if in == nil {
		return nil, nil
	}

	// TODO: currently there is a bug, we need to reset 'd.c's state
	// if the current timestamp changes. With configured BatchSize we
	// dont see all samples for a timestamp in the same batch, but this
	// logic relies on that.
	if len(d.p) > 0 {
		for i := range d.p {
			d.c[d.p[i].a] = 0
			d.c[d.p[i].b] = 0
		}
		for i, sv := range in {
			for _, sid := range sv.SampleIDs {
				d.c[sid] |= 2 << i
			}
		}
		for i := range d.p {
			if d.c[d.p[i].a]&d.c[d.p[i].b] > 0 {
				return nil, extlabels.ErrDuplicateLabelSet
			}
		}
	}

	return in, nil
}

func (d *duplicateLabelCheckOperator) Series(ctx context.Context) ([]labels.Labels, error) {
	start := time.Now()
	defer func() { d.AddSeriesExecutionTime(time.Since(start)) }()

	if err := d.init(ctx); err != nil {
		return nil, err
	}
	series, err := d.next.Series(ctx)
	if err != nil {
		return nil, err
	}
	d.SetMaxSeriesCount(int64(len(series)))
	return series, nil
}

func (d *duplicateLabelCheckOperator) GetPool() *model.VectorPool {
	return d.next.GetPool()
}

func (d *duplicateLabelCheckOperator) Explain() (next []model.VectorOperator) {
	return []model.VectorOperator{d.next}
}

func (d *duplicateLabelCheckOperator) String() string {
	return "[duplicateLabelCheck]"
}

func (d *duplicateLabelCheckOperator) init(ctx context.Context) error {
	var err error
	d.once.Do(func() {
		series, seriesErr := d.next.Series(ctx)
		if seriesErr != nil {
			err = seriesErr
			return
		}
		m := make(map[uint64]int, len(series))
		p := make([]pair, 0)
		c := make([]uint64, len(series))
		for i := range series {
			h := series[i].Hash()
			if j, ok := m[h]; ok {
				p = append(p, pair{a: i, b: j})
			} else {
				m[h] = i
			}
		}
		d.p = p
		d.c = c
	})
	return err
}
