package output

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/log"
)

func TestNewManager_InteractiveOnlyForInfoTTY(t *testing.T) {
	original := isTerminalWriter
	t.Cleanup(func() {
		isTerminalWriter = original
	})
	isTerminalWriter = func(io.Writer) bool { return true }

	infoManager := NewManager(&bytes.Buffer{}, log.InfoLevel)
	if _, ok := infoManager.NewSyncProgress().(*interactiveProgress); !ok {
		t.Fatal("info manager did not enable interactive progress")
	}

	debugManager := NewManager(&bytes.Buffer{}, log.DebugLevel)
	if debugManager.NewSyncProgress().Enabled() {
		t.Fatal("debug manager enabled interactive progress")
	}
}

func TestNewManager_DisablesInteractiveProgressWhenNotTTY(t *testing.T) {
	original := isTerminalWriter
	t.Cleanup(func() {
		isTerminalWriter = original
	})
	isTerminalWriter = func(io.Writer) bool { return false }

	manager := NewManager(&bytes.Buffer{}, log.InfoLevel)
	if manager.NewSyncProgress().Enabled() {
		t.Fatal("non-TTY manager enabled interactive progress")
	}
}

func TestInteractiveLogWriterClearsAndRedrawsPrompt(t *testing.T) {
	renderer := &interactiveRenderer{writer: &bytes.Buffer{}}
	buffer := renderer.writer.(*bytes.Buffer)
	renderer.SetLine("| Matching: Matching tracks 10/20")

	writer := &logWriter{renderer: renderer}
	if _, err := writer.Write([]byte("WARN message\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got := buffer.String()
	if !strings.Contains(got, "\033[2K") {
		t.Fatalf("output %q does not contain line clear sequence", got)
	}
	if !strings.Contains(got, "WARN message") {
		t.Fatalf("output %q does not contain log message", got)
	}
	if !strings.HasSuffix(got, "\r| Matching: Matching tracks 10/20") {
		t.Fatalf("output %q does not redraw the prompt", got)
	}
}

func TestFormatSummary_IncludesKeyCounts(t *testing.T) {
	got := formatSummary(Summary{
		Pushed:            2,
		Pulled:            3,
		Skipped:           4,
		ConflictsResolved: 1,
		Matched:           9,
		Unmatched:         2,
		NoResults:         1,
		Ambiguous:         1,
		Warnings:          5,
		Errors:            6,
		DryRun:            true,
		UnmatchedEntries: []SummaryUnmatched{
			{Path: "Artist/Album/Missing.mp3", Reason: "search returned no song candidates"},
		},
	})

	for _, want := range []string{
		"Sync complete",
		"matched 9",
		"pushed 2",
		"pulled 3",
		"skipped 4",
		"conflicts 1",
		"unmatched 2",
		"no-results 1",
		"ambiguous 1",
		"dry-run",
		"unmatched songs",
		"Artist/Album/Missing.mp3",
		"search returned no song candidates",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary %q does not contain %q", got, want)
		}
	}
}
