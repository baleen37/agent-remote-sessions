package tui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/session"
	"github.com/charmbracelet/x/ansi"
)


func TestAdjustSplitGrowsAndShrinksByStep(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live"), nil
	})
	start := value.previewPct
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: '>', Text: ">"}))
	if value.previewPct != start+previewPctStep {
		t.Fatalf("previewPct after > = %d, want %d", value.previewPct, start+previewPctStep)
	}
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: '<', Text: "<"}))
	if value.previewPct != start {
		t.Fatalf("previewPct after </> round trip = %d, want %d", value.previewPct, start)
	}
}

func TestAdjustSplitClampsAtBounds(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live"), nil
	})
	value.previewPct = previewPctMax
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: '>', Text: ">"}))
	if value.previewPct != previewPctMax {
		t.Fatalf("previewPct = %d, want clamped at max %d", value.previewPct, previewPctMax)
	}

	value.previewPct = previewPctMin
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: '<', Text: "<"}))
	if value.previewPct != previewPctMin {
		t.Fatalf("previewPct = %d, want clamped at min %d", value.previewPct, previewPctMin)
	}
}

func TestAdjustSplitNoopWhenPreviewHidden(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live"), nil
	})
	value, _ = updateModel(value, tea.KeyPressMsg(tea.Key{Code: 'p', Text: "p"})) // hide preview
	if value.previewVisible() {
		t.Fatal("preview should be hidden after p")
	}
	start := value.previewPct
	value, command := updateModel(value, tea.KeyPressMsg(tea.Key{Code: '>', Text: ">"}))
	if value.previewPct != start {
		t.Fatalf("previewPct changed to %d while preview hidden, want unchanged %d", value.previewPct, start)
	}
	if command != nil {
		t.Fatal("> while preview hidden should not schedule a command")
	}
}

// TestAdjustSplitSchedulesSaveWithNewPercentage calls adjustSplit directly
// and invokes only its save command (not the full key-press batch), so the
// splitFlashTick command sharing that batch never has to actually sleep out
// its real 1.5s duration.
func TestAdjustSplitSchedulesSaveWithNewPercentage(t *testing.T) {
	var saved int
	saveCalled := false
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live"), nil
	})
	value.deps.SavePreviewPct = func(pct int) error {
		saveCalled = true
		saved = pct
		return nil
	}
	value, _ = value.adjustSplit(true)
	command := value.saveSplitCmd()
	if command == nil {
		t.Fatal("saveSplitCmd() returned nil despite SavePreviewPct being wired")
	}
	result := command()
	message, ok := result.(savePreviewPctMsg)
	if !ok {
		t.Fatalf("saveSplitCmd() produced %#v, want savePreviewPctMsg", result)
	}
	if !saveCalled {
		t.Fatal("SavePreviewPct was not called")
	}
	if saved != value.previewPct {
		t.Fatalf("SavePreviewPct called with %d, want current previewPct %d", saved, value.previewPct)
	}
	if message.err != nil {
		t.Fatalf("savePreviewPctMsg.err = %v, want nil", message.err)
	}
}

func TestSavePreviewPctFailureShowsStatus(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live"), nil
	})
	value, _ = updateModel(value, savePreviewPctMsg{err: errors.New("disk full")})
	if !strings.HasPrefix(value.status, "save split ratio failed: ") {
		t.Fatalf("status = %q, want a %q prefix", value.status, "save split ratio failed: ")
	}
	if !strings.Contains(value.status, "disk full") {
		t.Fatalf("status = %q, want it to mention the underlying error", value.status)
	}
}

// TestSplitFlashShowsPercentageThenClearsAfterTimeout drives adjustSplit
// directly and injects splitFlashMsg with the seq it armed instead of
// waiting out splitFlashTick's real 1.5s duration — matching the kill_test.go
// convention of injecting killFireMsg{seq} rather than sleeping through
// killTick's grace period.
func TestSplitFlashShowsPercentageThenClearsAfterTimeout(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live"), nil
	})
	value, _ = value.adjustSplit(true)
	if !value.splitFlash {
		t.Fatal("splitFlash should be armed right after >")
	}
	content := ansi.Strip(value.View().Content)
	wantPreviewPct := value.previewPct
	wantListPct := 100 - value.previewPct
	if !strings.Contains(content, "PREVIEW") || !strings.Contains(content, strconv.Itoa(wantPreviewPct)+"%") {
		t.Fatalf("flash view missing PREVIEW percentage %d%%:\n%s", wantPreviewPct, content)
	}
	if !strings.Contains(content, strconv.Itoa(wantListPct)+"%") {
		t.Fatalf("flash view missing SESSIONS percentage %d%%:\n%s", wantListPct, content)
	}

	value, _ = updateModel(value, splitFlashMsg{seq: value.splitFlashSeq})
	if value.splitFlash {
		t.Fatal("splitFlash should clear once its timer fires")
	}
	content = ansi.Strip(value.View().Content)
	if strings.Contains(content, strconv.Itoa(wantPreviewPct)+"%") {
		t.Fatalf("flash percentage still rendered after timeout:\n%s", content)
	}
}

// TestSplitFlashRapidAdjustmentsOnlyLastTimerClears locks in the seq guard:
// firing the first (stale) timer after a second adjustment must not clear the
// flash the second adjustment armed, mirroring killDoneMsg's seq guard.
func TestSplitFlashRapidAdjustmentsOnlyLastTimerClears(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live"), nil
	})
	value, _ = value.adjustSplit(true)
	firstSeq := value.splitFlashSeq
	value, _ = value.adjustSplit(true)
	secondSeq := value.splitFlashSeq
	if firstSeq == secondSeq {
		t.Fatal("second adjustSplit should have advanced splitFlashSeq")
	}

	// The stale first timer fires after the second adjustment armed a new seq.
	value, _ = updateModel(value, splitFlashMsg{seq: firstSeq})
	if !value.splitFlash {
		t.Fatal("stale timer cleared the flash the second adjustment armed")
	}

	value, _ = updateModel(value, splitFlashMsg{seq: secondSeq})
	if value.splitFlash {
		t.Fatal("the matching (second) timer should clear the flash")
	}
}

// TestSplitFlashClearsRenderWhenPreviewHiddenByResize covers the PR #48
// lesson: a flash armed while the preview was visible must not leave a
// stale "NN%" title once a WindowSizeMsg narrows the terminal below
// previewMinWidth and hides the preview, even before the flash timer fires.
func TestSplitFlashClearsRenderWhenPreviewHiddenByResize(t *testing.T) {
	value := previewModel(func(context.Context, session.Session) ([]byte, error) {
		return []byte("live"), nil
	})
	value, _ = value.adjustSplit(true)
	if !value.splitFlash {
		t.Fatal("splitFlash should be armed right after >")
	}
	value, _ = updateModel(value, tea.WindowSizeMsg{Width: stackedMinWidth - 1, Height: value.height})
	content := ansi.Strip(value.View().Content)
	if strings.Contains(content, "%") {
		t.Fatalf("split flash percentage still rendered after preview hidden by resize:\n%s", content)
	}
}

