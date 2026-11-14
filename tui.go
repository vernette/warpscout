package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type (
	stepMsg struct {
		label, summary string
		done, fail     bool
	}
	barBeginMsg struct {
		label string
		total int
	}
	probedMsg struct{}
	foundMsg  struct {
		endpoint string
		epPing   time.Duration
		tunPing  time.Duration
		loss     float32
		measured bool
		exit     string
		colo     string
		meta     metaResult
		torn     bool
	}
	speedMsg struct {
		endpoint string
		mbps     float64
	}
	barEndMsg struct{ label, summary string }
	doneMsg   struct{}
)

type emitter func(tea.Msg)

var (
	plainNodes sync.Map
	plainOut   io.Writer = os.Stderr
	plainTotal atomic.Int64
	plainStep  atomic.Int64
	plainDone  atomic.Int64
)

func plainEmit(msg tea.Msg) {
	switch m := msg.(type) {
	case stepMsg:
		switch {
		case m.fail:
			fmt.Fprintln(plainOut, m.summary)
		case m.done:
			fmt.Fprintf(plainOut, "%s: %s\n", m.label, m.summary)
		default:
			fmt.Fprintln(plainOut, m.label+"...")
		}
	case barBeginMsg:
		plainTotal.Store(int64(m.total))
		plainStep.Store(int64(max(1, m.total/plainProgressSteps)))
		plainDone.Store(0)
		fmt.Fprintf(plainOut, "%s: %d...\n", m.label, m.total)
	case probedMsg:
		step, total := plainStep.Load(), plainTotal.Load()
		if step == 0 {
			return
		}
		if done := plainDone.Add(1); done%step == 0 && done < total {
			fmt.Fprintf(plainOut, "  %d/%d\n", done, total)
		}
	case foundMsg:
		if m.exit == "" || m.torn {
			return
		}
		if _, seen := plainNodes.LoadOrStore(m.exit+" "+m.colo, true); !seen {
			fmt.Fprintf(plainOut, "  node: %s %s (%s)\n", m.exit, m.colo, m.endpoint)
		}
	case speedMsg:
		fmt.Fprintf(plainOut, "  %-22s %s\n", m.endpoint, speedStr(m.mbps))
	case barEndMsg:
		fmt.Fprintf(plainOut, "%s: %s\n", m.label, m.summary)
	}
}

const (
	plainProgressSteps = 10

	feedMax        = 12
	nodeLineWidth  = 76
	nodeMax        = 24
	nodeGap        = 3
	nodeSep        = " · "
	minListRows    = 3
	listChrome     = 5
	barWidth       = 28
	minBarWidth    = 10
	barMargin      = 14
	minCountsWidth = 60
)

type nodeStat struct {
	exit, colo string
	n          int
}

type scanModel struct {
	cancel   context.CancelFunc
	ping     bool
	header   string
	quitHint string
	st       conStyles
	spin     spinner.Model
	bar      progress.Model

	width  int
	height int
	stepAt time.Time
	barAt  time.Time
	lines  []string
	step   string
	label  string
	total  int
	done   int
	feed   []foundMsg
	nodes  []nodeStat
	speeds []speedMsg

	feedFull  bool
	nodesAll  bool
	dropLists bool
	finished  bool
}

func newScanModel(cancel context.CancelFunc, ping bool) scanModel {
	st := newConStyles(lipgloss.NewRenderer(os.Stderr))
	sp := spinner.New()
	sp.Spinner = scanSpinner
	sp.Style = st.accent
	return scanModel{
		cancel:   cancel,
		ping:     ping,
		quitHint: "q to quit",
		st:       st,
		spin:     sp,
		bar:      progress.New(progress.WithDefaultGradient(), progress.WithWidth(barWidth)),
	}
}

func (m scanModel) Init() tea.Cmd { return m.spin.Tick }

func (m scanModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.bar.Width = min(barWidth, max(minBarWidth, msg.Width-barMargin))
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			if m.cancel != nil {
				m.cancel()
			}
			m.finished = true
			return m, tea.Quit
		case "f":
			m.feedFull = !m.feedFull
			return m, nil
		case "n":
			m.nodesAll = !m.nodesAll
			return m, nil
		}

	case stepMsg:
		switch {
		case msg.fail:
			m.step = ""
			m.lines = append(m.lines, m.st.fail.Render(glyphFail+" "+msg.summary))
		case msg.done:
			m.step = ""
			m.lines = append(m.lines, m.st.ok.Render(glyphOK+" ")+m.st.title.Render(msg.label)+": "+msg.summary+took(m.stepAt))
			m.stepAt = time.Time{}
		default:
			m.step, m.stepAt = msg.label, time.Now()
		}
		return m, nil

	case barBeginMsg:
		m.label, m.total, m.done, m.barAt = msg.label, msg.total, 0, time.Now()
		return m, m.bar.SetPercent(0)

	case probedMsg:
		if m.total > 0 {
			m.done++
			return m, m.bar.SetPercent(float64(m.done) / float64(m.total))
		}
		return m, nil

	case foundMsg:
		m.feed = insertSorted(m.feed, msg, lessLatency)
		m.nodes = countNode(m.nodes, msg)
		return m, nil

	case speedMsg:
		m.speeds = insertSorted(m.speeds, msg, func(a, b speedMsg) bool { return a.mbps > b.mbps })
		return m, nil

	case barEndMsg:
		m.label, m.total, m.done = "", 0, 0
		m.lines = append(m.lines, m.st.ok.Render(glyphOK+" ")+m.st.title.Render(msg.label)+": "+msg.summary+took(m.barAt))
		m.barAt = time.Time{}
		return m, nil

	case doneMsg:
		m.finished = true
		if m.dropLists {
			m.feed, m.speeds, m.nodes = nil, nil, nil
		}
		return m, tea.Quit

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case progress.FrameMsg:
		pm, cmd := m.bar.Update(msg)
		m.bar = pm.(progress.Model)
		return m, cmd
	}
	return m, nil
}

func insertSorted[T any](list []T, v T, less func(a, b T) bool) []T {
	i := sort.Search(len(list), func(i int) bool { return less(v, list[i]) })
	list = append(list, v)
	copy(list[i+1:], list[i:])
	list[i] = v
	return list
}

func countNode(nodes []nodeStat, msg foundMsg) []nodeStat {
	if msg.exit == "" || msg.torn {
		return nodes
	}
	found := false
	for i := range nodes {
		if nodes[i].exit == msg.exit && nodes[i].colo == msg.colo {
			nodes[i].n++
			found = true
			break
		}
	}
	if !found {
		nodes = append(nodes, nodeStat{exit: msg.exit, colo: msg.colo, n: 1})
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].n != nodes[j].n {
			return nodes[i].n > nodes[j].n
		}
		if nodes[i].exit != nodes[j].exit {
			return nodes[i].exit < nodes[j].exit
		}
		return nodes[i].colo < nodes[j].colo
	})
	return nodes
}

func lessLatency(a, b foundMsg) bool {
	if a.loss != b.loss {
		return a.loss < b.loss
	}
	pa, pb := a.sortPing(), b.sortPing()
	if (pa <= 0) != (pb <= 0) {
		return pa > 0
	}
	if pa != pb {
		return pa < pb
	}
	return a.endpoint < b.endpoint
}

func (m foundMsg) sortPing() time.Duration {
	if m.measured {
		return m.tunPing
	}
	return m.epPing
}

func (m foundMsg) lossStr() string {
	if !m.measured {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", m.loss*100)
}

func (m scanModel) View() string {
	st := m.st
	full := m.feedFull && len(m.feed) > 0
	var b strings.Builder
	if m.header != "" && !full {
		b.WriteString(st.title.Render(m.header) + "\n\n")
	}

	if !full {
		for _, l := range m.lines {
			b.WriteString(l + "\n")
		}
	}
	if m.step != "" && !full {
		b.WriteString(m.spin.View() + " " + st.title.Render(m.step) + "\n")
	}
	if m.total > 0 {
		b.WriteString(m.spin.View() + " " + st.title.Render(m.label) + "\n")
		b.WriteString("  " + m.bar.View() + fmt.Sprintf("  %d/%d", m.done, m.total) + st.dim.Render(m.counts()+since(m.barAt)) + "\n")
	}

	nodes := ""
	if len(m.nodes) > 0 && !full {
		nodes = "\n" + m.renderNodes()
	}
	rows := m.listRows(strings.Count(b.String(), "\n") + strings.Count(nodes, "\n"))

	if len(m.feed) > 0 {
		b.WriteString("\n" + m.renderFeed(rows))
	}
	b.WriteString(nodes)
	if len(m.speeds) > 0 {
		b.WriteString("\n" + m.renderSpeeds(rows))
	}
	if !m.finished {
		b.WriteString("\n" + st.dim.Render(m.hint()) + "\n")
	}
	return b.String()
}

func since(start time.Time) string {
	if start.IsZero() {
		return ""
	}
	d := time.Since(start).Truncate(time.Second)
	if d < time.Second {
		return ""
	}
	return "  " + d.String()
}

func took(start time.Time) string {
	if e := since(start); e != "" {
		return " in" + e[1:]
	}
	return ""
}

func (m scanModel) counts() string {
	if len(m.feed) == 0 || (m.width > 0 && m.width < minCountsWidth) {
		return ""
	}
	torn := 0
	for _, r := range m.feed {
		if r.torn {
			torn++
		}
	}
	s := fmt.Sprintf("   working %d", len(m.feed)-torn)
	if torn > 0 {
		s += fmt.Sprintf(" · torn %d", torn)
	}
	if len(m.nodes) > 0 {
		s += fmt.Sprintf(" · nodes %d", len(m.nodes))
	}
	return s
}

func (m scanModel) hint() string {
	keys := ""
	if len(m.feed) > 0 {
		keys += " · f for the feed alone"
	}
	if len(m.nodes) > nodeMax {
		keys += " · n for every node"
	}
	return m.quitHint + keys
}

func (m scanModel) listRows(used int) int {
	if m.height <= 0 {
		return feedMax
	}
	rows := max(minListRows, m.height-used-listChrome)
	if m.feedFull {
		return rows
	}
	return min(feedMax, rows)
}

func pad(s string, n int) string { return fmt.Sprintf("%-*s", n, s) }

func (m scanModel) renderSpeeds(limit int) string {
	st := m.st
	rows, extra := capped(m.speeds, limit)
	var b strings.Builder
	b.WriteString(st.dim.Render(pad("ENDPOINT", 22)+" SPEED") + "\n")
	for _, r := range rows {
		style := st.accent
		if r.mbps <= 0 {
			style = st.warn
		}
		b.WriteString(st.title.Render(pad(r.endpoint, 22)) + " " + style.Render(speedStr(r.mbps)) + "\n")
	}
	writeFeedRest(&b, st, extra)
	return b.String()
}

func capped[T any](rows []T, n int) ([]T, int) {
	if len(rows) <= n {
		return rows, 0
	}
	return rows[:n], len(rows) - n
}

func writeFeedRest(b *strings.Builder, st conStyles, extra int) {
	if extra > 0 {
		b.WriteString(st.dim.Render(fmt.Sprintf("… +%d more", extra)) + "\n")
	}
}

func (m scanModel) renderNodes() string {
	st := m.st
	limit := nodeMax
	if m.nodesAll {
		limit = len(m.nodes)
	}
	rows, extra := capped(m.nodes, limit)
	wExit, wColo, wCount := 0, 0, 0
	for _, r := range rows {
		wExit = max(wExit, lipgloss.Width(r.exit))
		wColo = max(wColo, len(r.colo))
		wCount = max(wCount, len(strconv.Itoa(r.n)))
	}
	width := nodeLineWidth
	if m.width > 0 {
		width = m.width
	}
	cols := max(1, (width+nodeGap)/(wExit+wColo+wCount+lipgloss.Width(nodeSep)+1+nodeGap))

	var b strings.Builder
	b.WriteString(st.dim.Render("NODES") + "\n")
	for i, r := range rows {
		exit := r.exit + strings.Repeat(" ", wExit-lipgloss.Width(r.exit))
		b.WriteString(st.title.Render(exit) + st.dim.Render(nodeSep) + pad(r.colo, wColo) + " " + st.accent.Render(fmt.Sprintf("%*d", wCount, r.n)))
		if i%cols == cols-1 || i == len(rows)-1 {
			b.WriteString("\n")
			continue
		}
		b.WriteString(strings.Repeat(" ", nodeGap))
	}
	writeFeedRest(&b, st, extra)
	return b.String()
}

func (m scanModel) renderFeed(limit int) string {
	st := m.st
	rows, extra := capped(m.feed, limit)
	tunHead := ""
	if m.ping {
		tunHead = pad("TUN PING", 9) + " " + pad("LOSS", 6) + " "
	}
	var b strings.Builder
	b.WriteString(st.dim.Render(pad("ENDPOINT", 22)+" "+pad("ENDPOINT PING", 13)+" "+tunHead+pad("SEEN AS", 10)+" NODE") + "\n")
	for _, r := range rows {
		ep := pad(r.endpoint, 22)
		ping := pad(latencyStr(r.epPing), 13)
		tun := ""
		if m.ping {
			tun = pad(latencyStr(r.tunPing), 9) + " " + pad(r.lossStr(), 6) + " "
		}
		if r.torn {
			b.WriteString(st.warn.Render(ep+" "+ping+" "+tun+"torn down") + "\n")
			continue
		}
		exit := r.exit + strings.Repeat(" ", max(0, 10-lipgloss.Width(r.exit)))
		tunSeg := ""
		if m.ping {
			tunSeg = st.accent.Render(tun)
		}
		b.WriteString(st.title.Render(ep) + " " + st.accent.Render(ping) + " " + tunSeg + exit + " " + r.colo + "\n")
	}
	writeFeedRest(&b, st, extra)
	return b.String()
}
