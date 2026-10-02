package notify

import "testing"

func TestResolveNeonStorage(t *testing.T) {
	logical := neonV3Metric{Name: "root_branch_logical_size", Usage: 35799040}
	history := neonV3Metric{Name: "root_branch_history_size", Usage: 4319656}
	child := neonV3Metric{Name: "child_branch_change_size", Usage: 0}
	unrelated := neonV3Metric{Name: "some_future_metric", Usage: 999999999}

	cases := []struct {
		name    string
		peak    float64
		data    float64
		metrics []neonV3Metric
		want    float64
	}{
		{"v3.1 peak wins", 440232448, 0, nil, 440232448},
		{"v3.1 data fallback", 0, 123456, nil, 123456},
		{"v3.2 zeroed legacy -> v3 sum", 0, 0,
			[]neonV3Metric{logical, history, child}, 35799040 + 4319656},
		{"v3.2 ignores unknown metrics", 0, 0,
			[]neonV3Metric{logical, unrelated}, 35799040},
		{"nothing available", 0, 0, nil, 0},
		{"legacy preferred over v3", 440232448, 0,
			[]neonV3Metric{logical, history}, 440232448},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveNeonStorage(c.peak, c.data, c.metrics); got != c.want {
				t.Errorf("resolveNeonStorage(%v, %v, %v) = %v, want %v",
					c.peak, c.data, c.metrics, got, c.want)
			}
		})
	}
}
