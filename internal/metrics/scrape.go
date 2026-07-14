package metrics

import (
	"context"
	"fmt"
	"io"
	"net/http"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// Exposition is a parsed /metrics scrape: the metric families keyed by name. It
// exists so e2e assertions can look up a per-path series value without each test
// re-implementing the Prometheus text-format parser.
type Exposition struct {
	families map[string]*dto.MetricFamily
}

// Fetch GETs baseURL (e.g. "http://127.0.0.1:9095/metrics") with client and
// parses the response into an Exposition. It is the scrape helper future e2e
// tests use to drive assertions against a running endpoint.
func Fetch(ctx context.Context, client *http.Client, baseURL string) (Exposition, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	if err != nil {
		return Exposition{}, fmt.Errorf("metrics: build scrape request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return Exposition{}, fmt.Errorf("metrics: scrape %q: %w", baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Exposition{}, fmt.Errorf("metrics: scrape %q: status %s", baseURL, resp.Status)
	}

	// v0.66's TextParser requires an explicit name-validation scheme; the
	// zero-value parser leaves it unset and panics. UTF8Validation matches the
	// promhttp exposition this endpoint serves.
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		return Exposition{}, fmt.Errorf("metrics: parse exposition: %w", err)
	}
	// Drain any residue so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	return Exposition{families: families}, nil
}

// Families returns the parsed metric families keyed by metric name.
func (e Exposition) Families() map[string]*dto.MetricFamily { return e.families }

// Has reports whether the named metric family is present (registered), regardless
// of its value — useful for asserting the placeholder FEC series exist.
func (e Exposition) Has(name string) bool {
	_, ok := e.families[name]
	return ok
}

// PathValue returns the value of the per-path series `name` for the given path
// label, and whether such a series was found. It reads whichever of the
// gauge/counter value fields the sample carries.
func (e Exposition) PathValue(name, path string) (float64, bool) {
	return e.labeledValue(name, labelPath, path)
}

// PeerValue returns the value of a per-peer series `name` (FEC/resequencer, T94) for
// the given `peer` label value, and whether such a series was found.
func (e Exposition) PeerValue(name, peer string) (float64, bool) {
	return e.labeledValue(name, labelPeer, peer)
}

// PeerPathValue returns the value of a per-(peer,path) series `name` (T94) for the
// given peer+path label pair, and whether such a series was found.
func (e Exposition) PeerPathValue(name, peer, path string) (float64, bool) {
	fam, ok := e.families[name]
	if !ok {
		return 0, false
	}
	for _, m := range fam.GetMetric() {
		var gotPeer, gotPath string
		var havePeer, havePath bool
		for _, lp := range m.GetLabel() {
			switch lp.GetName() {
			case labelPeer:
				gotPeer, havePeer = lp.GetValue(), true
			case labelPath:
				gotPath, havePath = lp.GetValue(), true
			}
		}
		if havePeer && havePath && gotPeer == peer && gotPath == path {
			return metricValue(m), true
		}
	}
	return 0, false
}

// Value returns the value of an unlabeled series `name` (e.g. the FEC
// placeholders), and whether it was found.
func (e Exposition) Value(name string) (float64, bool) {
	fam, ok := e.families[name]
	if !ok {
		return 0, false
	}
	for _, m := range fam.GetMetric() {
		if len(m.GetLabel()) == 0 {
			return metricValue(m), true
		}
	}
	return 0, false
}

func (e Exposition) labeledValue(name, labelName, labelValue string) (float64, bool) {
	fam, ok := e.families[name]
	if !ok {
		return 0, false
	}
	for _, m := range fam.GetMetric() {
		for _, lp := range m.GetLabel() {
			if lp.GetName() == labelName && lp.GetValue() == labelValue {
				return metricValue(m), true
			}
		}
	}
	return 0, false
}

// metricValue extracts the scalar value from a dto.Metric, reading whichever of
// the gauge/counter/untyped fields is populated.
func metricValue(m *dto.Metric) float64 {
	switch {
	case m.GetGauge() != nil:
		return m.GetGauge().GetValue()
	case m.GetCounter() != nil:
		return m.GetCounter().GetValue()
	case m.GetUntyped() != nil:
		return m.GetUntyped().GetValue()
	default:
		return 0
	}
}
