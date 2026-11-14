package main

import (
	"encoding/json"
	"io"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"
)

type jsonEndpoint struct {
	Endpoint  string  `json:"endpoint"`
	IP        string  `json:"ip,omitempty"`
	Port      int     `json:"port,omitempty"`
	Node      string  `json:"node,omitempty"`
	City      string  `json:"city,omitempty"`
	Country   string  `json:"country,omitempty"`
	Location  string  `json:"location,omitempty"`
	PingMS    float64 `json:"ping_ms,omitempty"`
	TunPingMS float64 `json:"tun_ping_ms,omitempty"`
	Loss      float32 `json:"loss,omitempty"`
	SpeedMbps float64 `json:"speed_mbps,omitempty"`
	Durable   bool    `json:"durable"`
}

type jsonProgressEvent struct {
	Event string `json:"event"`
	Phase string `json:"phase,omitempty"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

type jsonErrorEvent struct {
	Event string `json:"event"`
	Error string `json:"error"`
}

type jsonEndpointEvent struct {
	Event string `json:"event"`
	jsonEndpoint
}

type jsonDone struct {
	Proto        string         `json:"proto"`
	Total        int            `json:"total"`
	WorkingCount int            `json:"working_count"`
	Best         string         `json:"best,omitempty"`
	Picks        []jsonEndpoint `json:"picks"`
	Working      []jsonEndpoint `json:"working"`
}

type jsonDoneEvent struct {
	Event string `json:"event"`
	jsonDone
}

func toJSONEndpoint(r endpointResult) jsonEndpoint {
	ep := jsonEndpoint{
		Endpoint:  r.endpoint,
		Node:      r.exit.colo,
		City:      r.exit.coloCity,
		Country:   r.exit.coloISO,
		Location:  r.exit.loc,
		Loss:      r.loss,
		SpeedMbps: r.speed,
		Durable:   r.durable,
	}
	if ap, err := netip.ParseAddrPort(r.endpoint); err == nil {
		ep.IP = ap.Addr().String()
		ep.Port = int(ap.Port())
	} else if r.ip.IsValid() {
		ep.IP = r.ip.String()
	}
	if r.epPing > 0 {
		ep.PingMS = float64(r.epPing.Microseconds()) / 1000.0
	}
	if r.tunPing > 0 {
		ep.TunPingMS = float64(r.tunPing.Microseconds()) / 1000.0
	}
	return ep
}

func makeJSONDone(ph phaseResult) jsonDone {
	working := workingSorted(ph.results)
	picks := nodePicks(working)

	d := jsonDone{
		Proto:        ph.run.name,
		Total:        len(ph.results),
		WorkingCount: len(working),
		Picks:        make([]jsonEndpoint, len(picks)),
		Working:      make([]jsonEndpoint, len(working)),
	}
	if best, ok := bestOverall(ph); ok {
		d.Best = best.endpoint
	}
	for i, p := range picks {
		d.Picks[i] = toJSONEndpoint(p)
	}
	for i, w := range working {
		d.Working[i] = toJSONEndpoint(w)
	}
	return d
}

type jsonStreamEmitter struct {
	mu    sync.Mutex
	enc   *json.Encoder
	total int
	phase string
	done  atomic.Int64
}

func newJSONStreamEmitter(out io.Writer) *jsonStreamEmitter {
	return &jsonStreamEmitter{
		enc:   json.NewEncoder(out),
		phase: "scan",
	}
}

func (e *jsonStreamEmitter) send(v any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	_ = e.enc.Encode(v)
}

func (e *jsonStreamEmitter) emit(msg tea.Msg) {
	switch m := msg.(type) {
	case stepMsg:
		if m.fail {
			e.send(jsonErrorEvent{
				Event: "error",
				Error: m.summary,
			})
		}
	case barBeginMsg:
		e.total = m.total
		e.done.Store(0)
		e.phase = "scan"
		if strings.HasPrefix(m.label, "Speedtest") {
			e.phase = "speed"
		}
		e.send(jsonProgressEvent{
			Event: "progress",
			Phase: e.phase,
			Done:  0,
			Total: m.total,
		})
	case probedMsg:
		d := int(e.done.Add(1))
		e.send(jsonProgressEvent{
			Event: "progress",
			Phase: e.phase,
			Done:  d,
			Total: e.total,
		})
	case foundMsg:
		if m.endpoint == "" {
			return
		}
		ep := jsonEndpoint{
			Endpoint: m.endpoint,
			Node:     m.colo,
			City:     m.meta.coloCity,
			Country:  m.meta.coloISO,
			Location: m.meta.loc,
			Loss:     m.loss,
			Durable:  !m.torn,
		}
		if ep.Location == "" {
			ep.Location = m.exit
		}
		if ap, err := netip.ParseAddrPort(m.endpoint); err == nil {
			ep.IP = ap.Addr().String()
			ep.Port = int(ap.Port())
		}
		if m.epPing > 0 {
			ep.PingMS = float64(m.epPing.Microseconds()) / 1000.0
		}
		if m.tunPing > 0 {
			ep.TunPingMS = float64(m.tunPing.Microseconds()) / 1000.0
		}
		e.send(jsonEndpointEvent{
			Event:        "endpoint",
			jsonEndpoint: ep,
		})
	}
}

func writeJSON(w io.Writer, ph phaseResult) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(makeJSONDone(ph))
}

func writeJSONStreamDone(w io.Writer, ph phaseResult) error {
	return json.NewEncoder(w).Encode(jsonDoneEvent{
		Event:    "done",
		jsonDone: makeJSONDone(ph),
	})
}
