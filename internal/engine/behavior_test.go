package engine

import (
	"testing"
	"time"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/config"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/state"
)

func TestInitialFullBuild(t *testing.T) {
	h := newHarness(t, sampleModules2(), 20*time.Millisecond, 4)
	defer h.stop(t)
	h.waitIdleEvent(t, time.Second)
	for _, m := range []string{"a", "b", "c", "d"} {
		s := h.st.Snapshot(m)
		if s.Status != state.StatusOK || s.VersionID == "" {
			t.Fatalf("%s not built: %+v", m, s)
		}
	}
}

func TestDependencyPropagationAndNoUnrelatedBuild(t *testing.T) {
	h := newHarness(t, sampleModules2(), 30*time.Millisecond, 4)
	defer h.stop(t)
	h.waitIdleEvent(t, time.Second)

	beforeD := h.b.callCount("d")
	before := map[string]int{}
	for _, m := range []string{"a", "b", "c"} {
		before[m] = h.b.callCount(m)
	}
	idle0 := h.idleEvents()

	h.write(t, "mod-c", "changed-c-1")
	h.eng.NotifyChanged([]string{"mod-c/src.txt"})
	h.waitNextIdleFrom(t, idle0, 3*time.Second)

	if h.b.callCount("c") <= before["c"] {
		t.Fatal("c was not rebuilt")
	}
	if h.b.callCount("b") <= before["b"] {
		t.Fatal("dependent b was not rebuilt")
	}
	if h.b.callCount("a") <= before["a"] {
		t.Fatal("transitive dependent a was not rebuilt")
	}
	if h.b.callCount("d") != beforeD {
		t.Fatalf("unrelated d rebuilt %d times", h.b.callCount("d")-beforeD)
	}

	var order []string
	for _, e := range h.eventsOf(EventBuildOK) {
		order = append(order, e.Module)
	}
	first := map[string]int{}
	for i, m := range order {
		if _, seen := first[m]; !seen {
			first[m] = i
		}
	}
	if !(first["c"] < first["b"] && first["b"] < first["a"]) {
		t.Fatalf("build order violated: %v", order)
	}
}

func TestBurstCoalescingAndFinalConsistency(t *testing.T) {
	h := newHarness(t, sampleModules2(), 40*time.Millisecond, 2)
	defer h.stop(t)
	h.waitIdleEvent(t, time.Second)

	// Editor save storm with DISTINCT content each time (mtime granularity
	// would otherwise merge identical writes). Final content wins.
	for i := 0; i < 50; i++ {
		h.write(t, "mod-c", "intermediate-"+itoa(i))
		h.eng.NotifyChanged([]string{"mod-c/src.txt"})
	}
	h.write(t, "mod-c", "final-c-content")
	h.eng.NotifyChanged([]string{"mod-c/src.txt"})
	h.waitQuiet(t, 3*time.Second)
	time.Sleep(200 * time.Millisecond) // let the drain loop settle

	snap := h.waitStatus(t, "c", state.StatusOK, time.Second)
	data, err := h.eng.art.Get(storeKey("c", snap.VersionID))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("empty artifact")
	}

	// A from-scratch engine over the same workspace must yield same versions.
	h2 := newHarnessIn(t, h.ws, ".artifacts-fresh", sampleModules2(), 10*time.Millisecond, 2)
	defer h2.stop(t)
	h2.waitIdleEvent(t, time.Second)
	for _, m := range []string{"a", "b", "c"} {
		s1 := h.st.Snapshot(m)
		s2 := h2.st.Snapshot(m)
		if s1.VersionID != s2.VersionID {
			t.Fatalf("module %s: incremental %s != full build %s", m, s1.VersionID, s2.VersionID)
		}
	}
}

func TestFailureIsolationRetentionAndRecovery(t *testing.T) {
	h := newHarness(t, sampleModules2(), 20*time.Millisecond, 4)
	defer h.stop(t)
	h.waitIdleEvent(t, time.Second)
	goodC := h.st.Snapshot("c")
	goodB := h.st.Snapshot("b")

	h.b.setFailing("c", true)
	idle0 := h.idleEvents()
	h.write(t, "mod-c", "broken-1")
	h.eng.NotifyChanged([]string{"mod-c/src.txt"})
	h.waitNextIdleFrom(t, idle0, 3*time.Second)

	cFail := h.waitStatus(t, "c", state.StatusFailed, time.Second)
	if cFail.VersionID != goodC.VersionID {
		t.Fatal("failed module must retain last good version")
	}
	if cFail.Error == "" {
		t.Fatal("failure must expose an error message")
	}
	if d := h.st.Snapshot("d"); d.Status != state.StatusOK || d.VersionID == "" {
		t.Fatalf("unrelated d must stay usable: %+v", d)
	}

	h.b.setFailing("c", false)
	idle1 := h.idleEvents()
	h.write(t, "mod-c", "fixed-1")
	h.eng.NotifyChanged([]string{"mod-c/src.txt"})
	h.waitNextIdleFrom(t, idle1, 3*time.Second)
	h.waitStatus(t, "c", state.StatusOK, time.Second)
	h.waitStatus(t, "b", state.StatusOK, time.Second)
	newB := h.st.Snapshot("b")
	if newB.VersionID == goodB.VersionID {
		t.Fatal("b should have rebuilt against fixed c")
	}
}

func TestSameContentNoNewVersion(t *testing.T) {
	h := newHarness(t, sampleModules2(), 20*time.Millisecond, 4)
	defer h.stop(t)
	h.waitIdleEvent(t, time.Second)
	s1 := h.st.Snapshot("c")
	callsBefore := h.b.callCount("c")

	h.write(t, "mod-c", "v0-c") // identical to initial content
	h.eng.NotifyChanged([]string{"mod-c/src.txt"})
	time.Sleep(300 * time.Millisecond)
	if h.b.callCount("c") != callsBefore {
		t.Fatalf("identical content triggered a build: %d vs %d", h.b.callCount("c"), callsBefore)
	}
	if h.st.Snapshot("c").VersionID != s1.VersionID {
		t.Fatal("version id changed for identical content")
	}
}

func TestChangesDuringBuildAreNotLost(t *testing.T) {
	mods := singleModule()
	h := newHarness(t, mods, 15*time.Millisecond, 1)
	defer h.stop(t)
	h.waitIdleEvent(t, time.Second)

	h.b.mu.Lock()
	h.b.slow = 150 * time.Millisecond
	h.b.mu.Unlock()

	idle0 := h.idleEvents()
	h.write(t, "mod-c", "during-1")
	h.eng.NotifyChanged([]string{"mod-c/src.txt"})
	time.Sleep(50 * time.Millisecond) // build has started
	h.write(t, "mod-c", "during-2-final")
	h.eng.NotifyChanged([]string{"mod-c/src.txt"})
	h.waitNextIdleFrom(t, idle0, 3*time.Second)

	h.b.mu.Lock()
	h.b.slow = 0
	h.b.mu.Unlock()
	time.Sleep(200 * time.Millisecond) // allow the follow-up pass to finish

	h2 := newHarnessIn(t, h.ws, ".artifacts-fresh", mods, 10*time.Millisecond, 1)
	defer h2.stop(t)
	h2.waitIdleEvent(t, time.Second)
	if h.st.Snapshot("c").VersionID != h2.st.Snapshot("c").VersionID {
		t.Fatal("change during build was lost: final version mismatch")
	}
}

func sampleModules2() []config.Module {
	return []config.Module{
		{Name: "a", Root: "mod-a", Deps: []string{"b"}},
		{Name: "b", Root: "mod-b", Deps: []string{"c"}},
		{Name: "c", Root: "mod-c"},
		{Name: "d", Root: "mod-d"},
	}
}

func singleModule() []config.Module {
	return []config.Module{{Name: "c", Root: "mod-c"}}
}

func (h *harness) waitNextIdleFrom(t *testing.T, from int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if h.idleEvents() > from {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatal("no subsequent idle event")
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
