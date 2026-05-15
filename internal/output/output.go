package output

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/charmbracelet/x/term"
)

var isTerminalWriter = func(w io.Writer) bool {
	file, ok := w.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(file.Fd())
}

type Summary struct {
	Pushed            int
	Pulled            int
	Skipped           int
	ConflictsResolved int
	Matched           int
	Unmatched         int
	NoResults         int
	Ambiguous         int
	Warnings          int
	Errors            int
	DryRun            bool
	UnmatchedEntries  []SummaryUnmatched
}

type SummaryUnmatched struct {
	Path   string
	Reason string
}

type SyncProgress interface {
	Enabled() bool
	StartScanning()
	UpdateScan(count int)
	StartConnecting()
	StartMatching(total, workers int)
	UpdateMatching(processed, total, matched, unmatched, ambiguous int)
	StartApplying(total int, dryRun bool)
	UpdateApplying(processed, total, pushed, pulled, failed int, dryRun bool)
	WritingReport(path string)
	PrintSummary(summary Summary)
	Close()
}

type Manager struct {
	writer      io.Writer
	interactive *interactiveRenderer
}

func NewManager(w io.Writer, level log.Level) *Manager {
	manager := &Manager{writer: w}
	if level == log.InfoLevel && isTerminalWriter(w) {
		manager.interactive = &interactiveRenderer{writer: w}
	}
	return manager
}

func (m *Manager) LogWriter() io.Writer {
	if m == nil || m.interactive == nil {
		if m == nil {
			return io.Discard
		}
		return m.writer
	}
	return &logWriter{renderer: m.interactive}
}

func (m *Manager) NewSyncProgress() SyncProgress {
	if m == nil || m.interactive == nil {
		return NoopProgress()
	}
	return newInteractiveProgress(m.interactive)
}

func NoopProgress() SyncProgress {
	return noopProgress{}
}

type logWriter struct {
	renderer *interactiveRenderer
}

func (w *logWriter) Write(p []byte) (int, error) {
	return w.renderer.SuspendWrite(p)
}

type interactiveRenderer struct {
	mu     sync.Mutex
	writer io.Writer
	line   string
	active bool
}

func (r *interactiveRenderer) SetLine(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.line = line
	r.active = line != ""
	r.renderLocked()
}

func (r *interactiveRenderer) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
	r.line = ""
	r.active = false
}

func (r *interactiveRenderer) SuspendWrite(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	wasActive := r.active
	if wasActive {
		r.clearLocked()
	}
	n, err := r.writer.Write(p)
	if wasActive {
		r.renderLocked()
	}
	return n, err
}

func (r *interactiveRenderer) Print(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
	r.line = ""
	r.active = false
	_, _ = io.WriteString(r.writer, text)
}

func (r *interactiveRenderer) clearLocked() {
	if !r.active {
		return
	}
	_, _ = io.WriteString(r.writer, "\r\033[2K")
}

func (r *interactiveRenderer) renderLocked() {
	r.clearLocked()
	if !r.active || r.line == "" {
		return
	}
	_, _ = io.WriteString(r.writer, "\r"+r.line)
}

type interactiveProgress struct {
	renderer *interactiveRenderer
	done     chan struct{}
	wg       sync.WaitGroup
	once     sync.Once

	mu        sync.Mutex
	stage     string
	detail    string
	scanCount int

	matchProcessed int
	matchTotal     int
	matched        int
	unmatched      int
	ambiguous      int

	applyProcessed int
	applyTotal     int
	appliedPushed  int
	appliedPulled  int
	applyFailures  int
	applyDryRun    bool

	frame int

	summaryPrinted bool
}

func newInteractiveProgress(renderer *interactiveRenderer) *interactiveProgress {
	p := &interactiveProgress{
		renderer: renderer,
		done:     make(chan struct{}),
		stage:    "Preparing",
		detail:   "Starting sync",
	}
	p.wg.Add(1)
	go p.loop()
	p.render()
	return p
}

func (p *interactiveProgress) Enabled() bool { return true }

func (p *interactiveProgress) StartScanning() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stage = "Scanning"
	p.detail = "Scanning local files"
	p.scanCount = 0
}

func (p *interactiveProgress) UpdateScan(count int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scanCount = count
	p.detail = fmt.Sprintf("Scanning local files | found %d", count)
}

func (p *interactiveProgress) StartConnecting() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stage = "Connecting"
	p.detail = "Connecting to Navidrome"
}

func (p *interactiveProgress) StartMatching(total, workers int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stage = "Matching"
	p.matchTotal = total
	p.matchProcessed = 0
	p.matched = 0
	p.unmatched = 0
	p.ambiguous = 0
	p.detail = fmt.Sprintf("Matching tracks 0/%d | matched 0 | unmatched 0 | ambiguous 0 | workers %d", total, workers)
}

func (p *interactiveProgress) UpdateMatching(processed, total, matched, unmatched, ambiguous int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.matchProcessed = processed
	p.matchTotal = total
	p.matched = matched
	p.unmatched = unmatched
	p.ambiguous = ambiguous
	p.detail = fmt.Sprintf(
		"Matching tracks %d/%d | matched %d | unmatched %d | ambiguous %d",
		processed,
		total,
		matched,
		unmatched,
		ambiguous,
	)
}

func (p *interactiveProgress) StartApplying(total int, dryRun bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stage = "Applying"
	p.applyProcessed = 0
	p.applyTotal = total
	p.appliedPushed = 0
	p.appliedPulled = 0
	p.applyFailures = 0
	p.applyDryRun = dryRun
	p.detail = p.applyDetailLocked()
}

func (p *interactiveProgress) UpdateApplying(processed, total, pushed, pulled, failed int, dryRun bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.applyProcessed = processed
	p.applyTotal = total
	p.appliedPushed = pushed
	p.appliedPulled = pulled
	p.applyFailures = failed
	p.applyDryRun = dryRun
	p.detail = p.applyDetailLocked()
}

func (p *interactiveProgress) WritingReport(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stage = "Reporting"
	p.detail = fmt.Sprintf("Writing JSON report %s", path)
}

func (p *interactiveProgress) PrintSummary(summary Summary) {
	p.once.Do(func() {
		close(p.done)
	})
	p.wg.Wait()
	p.mu.Lock()
	p.summaryPrinted = true
	p.mu.Unlock()
	p.renderer.Print(formatSummary(summary))
}

func (p *interactiveProgress) Close() {
	p.once.Do(func() {
		close(p.done)
	})
	p.wg.Wait()
	p.mu.Lock()
	summaryPrinted := p.summaryPrinted
	p.mu.Unlock()
	if summaryPrinted {
		return
	}
	p.renderer.Clear()
}

func (p *interactiveProgress) loop() {
	defer p.wg.Done()

	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			p.mu.Lock()
			p.frame++
			p.mu.Unlock()
			p.render()
		}
	}
}

func (p *interactiveProgress) render() {
	p.mu.Lock()
	line := p.composeLineLocked()
	p.mu.Unlock()
	p.renderer.SetLine(line)
}

func (p *interactiveProgress) composeLineLocked() string {
	spinnerFrames := []string{"|", "/", "-", "\\"}
	spinner := spinnerFrames[p.frame%len(spinnerFrames)]
	return fmt.Sprintf("%s %s: %s", spinner, p.stage, p.detail)
}

func (p *interactiveProgress) applyDetailLocked() string {
	mode := "Applying changes"
	if p.applyDryRun {
		mode = "Dry-run applying changes"
	}
	return fmt.Sprintf(
		"%s %d/%d | pushed %d | pulled %d | failed %d",
		mode,
		p.applyProcessed,
		p.applyTotal,
		p.appliedPushed,
		p.appliedPulled,
		p.applyFailures,
	)
}

type noopProgress struct{}

func (noopProgress) Enabled() bool                                { return false }
func (noopProgress) StartScanning()                               {}
func (noopProgress) UpdateScan(int)                               {}
func (noopProgress) StartConnecting()                             {}
func (noopProgress) StartMatching(int, int)                       {}
func (noopProgress) UpdateMatching(int, int, int, int, int)       {}
func (noopProgress) StartApplying(int, bool)                      {}
func (noopProgress) UpdateApplying(int, int, int, int, int, bool) {}
func (noopProgress) WritingReport(string)                         {}
func (noopProgress) PrintSummary(Summary)                         {}
func (noopProgress) Close()                                       {}

func formatSummary(summary Summary) string {
	header := style("Sync complete", ansiBold, ansiCyan)
	mode := "live run"
	if summary.DryRun {
		mode = "dry-run"
	}

	lines := []string{
		header,
		fmt.Sprintf(
			"  %s  matched %d  pushed %d  pulled %d  skipped %d",
			style("mode", ansiBold),
			summary.Matched,
			summary.Pushed,
			summary.Pulled,
			summary.Skipped,
		),
		fmt.Sprintf(
			"  conflicts %d  unmatched %d  no-results %d  ambiguous %d",
			summary.ConflictsResolved,
			summary.Unmatched,
			summary.NoResults,
			summary.Ambiguous,
		),
		fmt.Sprintf(
			"  %s  warnings %s  errors %s",
			mode,
			style(fmt.Sprintf("%d", summary.Warnings), ansiYellow),
			style(fmt.Sprintf("%d", summary.Errors), ansiRed),
		),
	}
	if len(summary.UnmatchedEntries) > 0 {
		lines = append(lines, fmt.Sprintf("  %s", style("unmatched songs", ansiBold)))
		for _, item := range summary.UnmatchedEntries {
			line := fmt.Sprintf("    - %s", item.Path)
			if item.Reason != "" {
				line += fmt.Sprintf(" (%s)", item.Reason)
			}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func style(text string, codes ...string) string {
	if len(codes) == 0 {
		return text
	}
	return "\033[" + strings.Join(codes, ";") + "m" + text + "\033[0m"
}

const (
	ansiBold   = "1"
	ansiCyan   = "36"
	ansiYellow = "33"
	ansiRed    = "31"
)
