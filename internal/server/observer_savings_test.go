package server

import (
	"errors"
	"testing"

	"github.com/goastian/midopress/internal/flows"
	"github.com/goastian/midopress/internal/pipeline"
	"github.com/goastian/midopress/internal/proxy"
	"github.com/goastian/midopress/internal/stats"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObserverSavingsExcludeFailuresInStatsAndFlows(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  pipeline.Outcome
		want int64
	}{
		{"complete", pipeline.Outcome{Reason: pipeline.Transformed, BytesIn: 1000, BytesOut: 400}, 600},
		{"upstream failure", pipeline.Outcome{Reason: pipeline.PassError, BytesIn: 500, Err: errors.New("upstream reset")}, 0},
		{"partial transformed response", pipeline.Outcome{Reason: pipeline.Transformed, BytesIn: 1000, BytesOut: 50, Err: pipeline.ErrClientWrite}, 0},
		{"complete expansion", pipeline.Outcome{Reason: pipeline.Transformed, BytesIn: 100, BytesOut: 150}, -50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{collector: stats.New(stats.Options{}), flows: flows.NewStore(4, 0)}
			s.flows.SetEnabled(true)
			o := observer{s: s}
			o.Observe(&proxy.Observation{Outcome: tc.out})
			totals := s.collector.Snapshot(0).Totals
			assert.Equal(t, uint64(tc.out.BytesIn), totals.BytesIn)
			assert.Equal(t, uint64(tc.out.BytesOut), totals.BytesOut)
			assert.Equal(t, tc.want, totals.Saved)
			list, _ := s.flows.List(flows.ListOptions{})
			require.Len(t, list, 1)
			assert.Equal(t, tc.out.BytesIn, list[0].BytesIn)
			assert.Equal(t, tc.out.BytesOut, list[0].BytesOut)
			assert.Equal(t, tc.want, list[0].Saved)
		})
	}
}
