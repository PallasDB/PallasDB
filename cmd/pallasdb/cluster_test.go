package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShouldBootstrapCluster(t *testing.T) {
	tests := []struct {
		name string
		opts clusterStartOptions
		want bool
	}{
		{
			name: "serf disabled and no grpc join bootstraps",
			opts: clusterStartOptions{serfEnabled: false},
			want: true,
		},
		{
			name: "serf disabled with grpc join does not bootstrap",
			opts: clusterStartOptions{serfEnabled: false, joinAddr: "localhost:50051"},
			want: false,
		},
		{
			name: "serf enabled and no joins bootstraps",
			opts: clusterStartOptions{serfEnabled: true},
			want: true,
		},
		{
			name: "serf enabled with serf join does not bootstrap",
			opts: clusterStartOptions{serfEnabled: true, serfJoinAddrs: []string{"localhost:7946"}},
			want: false,
		},
		{
			name: "serf enabled with grpc join does not bootstrap",
			opts: clusterStartOptions{serfEnabled: true, joinAddr: "localhost:50051"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, shouldBootstrapCluster(&tt.opts))
		})
	}
}
