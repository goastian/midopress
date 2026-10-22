package proxy

import "github.com/goastian/midopress/internal/metrics"

// Instrument registers the proxy's counters. It is meant to be called once.
func (p *Proxy) Instrument(reg *metrics.Registry) {
	counter := func(name, help string, f func(Stats) float64) {
		reg.NewCounterFunc(name, help, func() float64 { return f(p.Stats()) })
	}
	counter("midopress_requests_total", "Requests forwarded to origins.", func(s Stats) float64 { return float64(s.Requests) })
	counter("midopress_origin_bytes_total", "Body bytes read from origins.", func(s Stats) float64 { return float64(s.BytesIn) })
	counter("midopress_client_bytes_total", "Body bytes sent to clients.", func(s Stats) float64 { return float64(s.BytesOut) })
	counter("midopress_upstream_errors_total", "Requests that failed reaching or reading an origin.", func(s Stats) float64 { return float64(s.UpstreamErrors) })
	counter("midopress_aborted_total", "Responses cut because the origin failed after they started.", func(s Stats) float64 { return float64(s.Aborted) })
	counter("midopress_denied_total", "Requests refused by authentication or access control.", func(s Stats) float64 { return float64(s.Denied) })
	counter("midopress_tunnels_total", "CONNECT tunnels opened.", func(s Stats) float64 { return float64(s.Tunnels) })
	counter("midopress_tls_handshakes_total", "TLS handshakes with clients of intercepted tunnels.", func(s Stats) float64 { return float64(s.TLSHandshakes) })
	counter("midopress_tls_handshake_errors_total", "Failed handshakes of intercepted tunnels.", func(s Stats) float64 { return float64(s.TLSHandshakeErrs) })
	counter("midopress_auto_passthrough_total", "Hosts passed through from then on because the client rejected our certificate.", func(s Stats) float64 { return float64(s.AutoPassthrough) })
	reg.NewGaugeFunc("midopress_requests_in_flight", "Requests being served.", func() float64 { return float64(p.Stats().InFlight) })
	reg.NewGaugeFunc("midopress_tunnels_active", "Tunnels open now.", func() float64 { return float64(p.Stats().TunnelsActive) })
}
