// Package gui implements `flowstate ui`: a native desktop UI built on Fyne
// (pure Go widgets, no Electron, no browser, no webview). The layout is
// inspired by Whisperflow's floating pill — one big record button, a live
// level meter, and the cleaned text front and center — with Settings and
// History tabs that expose most of the CLI's knobs (device, trigger,
// prompt, model, output destinations, silence trimming, artifact storage,
// mute, auto-stop, paste delay, languages, vocabulary).
//
// Threading: Fyne widgets must tickle the UI thread, so every background
// goroutine (meter ticker, transcribe/cleanup, paste-delay countdown,
// global-hotkey watcher) funnels widget updates through fyne.Do.
//
// Key bindings (also printed in the Record tab footer):
//
//	R (outside text fields) or Ctrl+R — toggle record/stop.
//	Esc — cancel the in-flight recording (nothing is transcribed).
//	Ctrl+Enter — paste the current text into the focused app.
//	--hotkey (default f9) — toggle from any app. gohook provides this on
//	X11 sessions; on Wayland the desktop owns the key instead, via
//	`flowstate ui --install-shortcut` (a GNOME custom shortcut running
//	`flowstate ui --toggle`, which talks to the window over a per-user
//	socket). See ipc.go and shortcut.go.
//	Hold ptt_key while trigger=push-to-talk — hold-to-record, mirroring the
//	CLI's push-to-talk gesture.
package gui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
	hook "github.com/robotn/gohook"

	"github.com/mazen160/flowstate/internal/audio"
	"github.com/mazen160/flowstate/internal/config"
	"github.com/mazen160/flowstate/internal/mute"
	"github.com/mazen160/flowstate/internal/output"
	"github.com/mazen160/flowstate/internal/prompts"
)

// Options wires the GUI to the already-loaded config. Config is mutated
// live by the Settings tab (session-only; persist by editing the file at
// ConfigPath).
type Options struct {
	Version    string
	Config     *config.Config
	ConfigPath string
	APIKey     string
	// Hotkey is the toggle key: gohook spelling on X11 ("f9"), and the
	// accelerator source for --install-shortcut ("f9" -> "F9"). Empty
	// disables the toggle hotkey.
	Hotkey string
}

// Run builds the window and blocks until it closes or ctx is cancelled.
func Run(ctx context.Context, o Options) error {
	if o.Config == nil {
		return fmt.Errorf("gui: nil config")
	}
	if o.APIKey == "" {
		return fmt.Errorf("gui: missing API key (set GROQ_API_KEY)")
	}
	a := app.New()
	u := &uiApp{
		cfg:     o.Config,
		cfgPath: o.ConfigPath,
		apiKey:  o.APIKey,
		version: o.Version,
		hotkey:  normalizeKey(o.Hotkey),
		histSel: -1,
	}
	u.win = a.NewWindow("flowstate")
	u.build()
	u.win.Resize(fyne.NewSize(470, 700))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// `flowstate ui --toggle` (run by a desktop shortcut) reaches the
	// window through this socket. Unlike gohook it works on Wayland,
	// because the desktop grabs the key and runs the client command. A
	// second window would silently steal the first one's socket, so
	// serveToggle refuses instead of unlinking a live peer.
	stopToggle, err := serveToggle(runCtx, func() { fyne.Do(u.toggle) })
	if err != nil {
		return err
	}
	defer stopToggle()

	// One teardown path for window close, SIGINT, and SIGTERM. Dying
	// mid-recording would leave the speakers muted, which the user cannot
	// undo from inside a dead process.
	var once sync.Once
	shutdown := func() {
		once.Do(func() {
			u.mu.Lock()
			rec := u.recorder
			u.recorder = nil
			u.mu.Unlock()
			if rec != nil {
				_ = rec.Close()
			}
			_ = mute.Unmute()
			cancel()
		})
	}
	u.win.SetOnClosed(shutdown)
	go func() {
		<-runCtx.Done()
		shutdown()
		// Quit is a UI-thread call, like every other Fyne API.
		fyne.Do(a.Quit)
	}()

	go u.watchHotkeys(runCtx)
	u.refreshHistory()
	u.win.ShowAndRun()
	return nil
}

type state int

const (
	stIdle state = iota
	stRecording
	stProcessing
)

// uiApp owns the window, the live config, the recording state machine, and
// every widget the background goroutines poke (always via fyne.Do).
type uiApp struct {
	win     fyne.Window
	cfg     *config.Config
	cfgPath string
	apiKey  string
	version string
	hotkey  string

	mu        sync.Mutex
	state     state
	held      bool // a push-to-talk hold owns the current recording
	recorder  *audio.Recorder
	recStart  time.Time
	meterStop chan struct{}
	stopTimer *time.Timer

	sessions []session
	histSel  int // -1 = none

	status    *widget.Label
	meter     *widget.Label
	timerLbl  *widget.Label
	recordBtn *widget.Button
	result    *widget.Entry
	rawLbl    *widget.Label
	savedLbl  *widget.Label
	histList  *widget.List
	tabs      *container.AppTabs
}

// session is one finished (or seeded) recording shown in History.
type session struct {
	when    time.Time
	raw     string
	cleaned string
	dir     string
}

// ---------------------------------------------------------------------------
// layout
// ---------------------------------------------------------------------------

func (u *uiApp) build() {
	brand := widget.NewLabelWithStyle("◉ flowstate", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	ver := widget.NewLabel("v" + u.version)
	header := container.NewHBox(brand, ver)

	u.status = widget.NewLabel("● Ready — press R or hit Record.")
	u.timerLbl = widget.NewLabel("")
	u.meter = widget.NewLabel(levelBar(nil))
	meterRow := container.NewHBox(u.timerLbl, u.meter)

	u.recordBtn = widget.NewButton("● Record  (R)", func() { u.toggle() })

	u.result = widget.NewMultiLineEntry()
	u.result.Wrapping = fyne.TextWrapWord
	u.result.SetMinRowsVisible(6)
	u.result.SetPlaceHolder("Cleaned text lands here…")

	u.rawLbl = widget.NewLabel("")
	u.rawLbl.Wrapping = fyne.TextWrapWord

	copyBtn := widget.NewButton("Copy", func() { u.copyNow() })
	pasteBtn := widget.NewButton("Paste into app", func() { u.pasteNow() })
	clearBtn := widget.NewButton("Clear", func() {
		u.result.SetText("")
		u.rawLbl.SetText("")
		u.savedLbl.SetText("")
	})
	actions := container.NewHBox(copyBtn, pasteBtn, clearBtn)

	u.savedLbl = widget.NewLabel("")
	u.savedLbl.Wrapping = fyne.TextWrapWord

	footer := widget.NewLabel("R record/stop · Esc cancel · Ctrl+R toggle · Ctrl+Enter paste · " + u.hotkeyHint())
	footer.Wrapping = fyne.TextWrapWord

	recordTab := container.NewVBox(header, u.status, meterRow, u.recordBtn,
		widget.NewLabel("Result"), u.result, u.rawLbl, actions, u.savedLbl, footer)

	u.histList = widget.NewList(
		func() int { u.mu.Lock(); defer u.mu.Unlock(); return len(u.sessions) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, o fyne.CanvasObject) {
			u.mu.Lock()
			defer u.mu.Unlock()
			if int(id) >= len(u.sessions) {
				return
			}
			s := u.sessions[id]
			text := s.when.Format("15:04:05") + "  " + previewText(s.cleaned)
			if text == s.when.Format("15:04:05")+"  " {
				text = s.when.Format("15:04:05") + "  (no text)"
			}
			o.(*widget.Label).SetText(text)
		},
	)
	openBtn := widget.NewButton("Open folder", func() {
		u.mu.Lock()
		var dir string
		if u.histSel >= 0 && u.histSel < len(u.sessions) {
			dir = u.sessions[u.histSel].dir
		}
		u.mu.Unlock()
		if dir == "" {
			u.setStatus("No recording folder selected.")
			return
		}
		if err := openPath(dir); err != nil {
			u.setStatus("Could not open folder: " + err.Error())
		}
	})
	u.histList.OnSelected = func(id widget.ListItemID) {
		u.mu.Lock()
		if int(id) >= len(u.sessions) {
			u.mu.Unlock()
			return
		}
		s := u.sessions[id]
		u.histSel = int(id)
		u.mu.Unlock()
		fyne.Do(func() {
			u.result.SetText(s.cleaned)
			u.setRaw(s.raw)
			if s.dir != "" {
				u.savedLbl.SetText("Saved in " + s.dir)
			} else {
				u.savedLbl.SetText("")
			}
			u.tabs.SelectIndex(0)
		})
	}
	refreshBtn := widget.NewButton("Refresh", func() { u.refreshHistory() })
	histTab := container.NewBorder(
		container.NewHBox(openBtn, refreshBtn),
		nil, nil, nil, u.histList)

	settings := u.buildSettings()
	settingsScroll := container.NewVScroll(settings)

	u.tabs = container.NewAppTabs(
		container.NewTabItem("Record", recordTab),
		container.NewTabItem("History", histTab),
		container.NewTabItem("Settings", settingsScroll),
	)
	u.win.SetContent(u.tabs)

	u.win.Canvas().SetOnTypedKey(func(e *fyne.KeyEvent) {
		switch e.Name {
		case fyne.KeyEscape:
			u.cancelRecording()
		case fyne.KeyR:
			if _, isEntry := u.win.Canvas().Focused().(*widget.Entry); !isEntry {
				u.toggle()
			}
		}
	})
	u.win.Canvas().AddShortcut(&desktop.CustomShortcut{
		KeyName:  fyne.KeyR,
		Modifier: fyne.KeyModifierControl,
	}, func(fyne.Shortcut) { u.toggle() })
	u.win.Canvas().AddShortcut(&desktop.CustomShortcut{
		KeyName:  fyne.KeyReturn,
		Modifier: fyne.KeyModifierControl,
	}, func(fyne.Shortcut) { u.pasteNow() })
}

// hotkeyHint renders the footer/global hint for the configured hotkey.
// On Wayland the X11 hook sees nothing outside our own window, so the hint
// points at the desktop-owned shortcut instead of claiming a broken key.
func (u *uiApp) hotkeyHint() string {
	if u.hotkey == "" {
		return "no global hotkey (--hotkey)"
	}
	if OnWayland() {
		key := Accelerator(u.hotkey)
		return key + " via desktop shortcut — run `flowstate ui --install-shortcut` (any app, works on Wayland)"
	}
	return strings.ToUpper(u.hotkey) + " toggles globally (X11)"
}

// ---------------------------------------------------------------------------
// settings tab — one widget per portable CLI/config knob, applied live
// ---------------------------------------------------------------------------

func (u *uiApp) buildSettings() *fyne.Container {
	cfg := u.cfg

	deviceNames, deviceByName := u.deviceOptions()
	deviceSel := widget.NewSelect(deviceNames, func(name string) {
		if uid, ok := deviceByName[name]; ok {
			cfg.InputDevice = uid
			u.setStatus("Input device: " + name)
		}
	})
	u.selectDevice(deviceSel, deviceNames, deviceByName)
	refreshDev := widget.NewButton("Reload", func() {
		names, byName := u.deviceOptions()
		deviceSel.Options = names
		u.selectDevice(deviceSel, names, byName)
		deviceSel.Refresh()
	})

	triggerSel := widget.NewSelect([]string{"enter", "push-to-talk"}, func(s string) {
		cfg.Trigger = s
		u.setStatus("Trigger: " + s + " (restart the app for the global hold key to update)")
	})
	triggerSel.SetSelected(cfg.Trigger)
	pttEntry := widget.NewEntry()
	pttEntry.SetText(cfg.PTTKey)
	pttEntry.OnChanged = func(s string) { cfg.PTTKey = strings.TrimSpace(s) }

	promptKeys := make([]string, 0, len(cfg.Prompts))
	for k := range cfg.Prompts {
		promptKeys = append(promptKeys, k)
	}
	sort.Strings(promptKeys)
	promptSel := widget.NewSelect(promptKeys, func(s string) {
		cfg.ActivePrompt = s
		u.setStatus("Prompt: " + s)
	})
	if len(promptKeys) > 0 {
		promptSel.SetSelected(cfg.ActivePrompt)
	}

	modelEntry := widget.NewEntry()
	modelEntry.SetText(cfg.CleanupModel)
	modelEntry.OnChanged = func(s string) { cfg.CleanupModel = strings.TrimSpace(s) }

	_, clipOn, pasteOn := cfg.OutputDestinations()
	var sel []string
	if clipOn {
		sel = append(sel, "clipboard")
	}
	if pasteOn {
		sel = append(sel, "paste")
	}
	var outGroup *widget.CheckGroup
	outGroup = widget.NewCheckGroup([]string{"clipboard", "paste"}, func(picked []string) {
		want := map[string]bool{}
		for _, p := range picked {
			want[p] = true
		}
		if len(picked) == 0 {
			// output_mode must name at least one destination; snap
			// back rather than persisting an invalid config.
			outGroup.SetSelected([]string{"clipboard"})
			cfg.OutputMode = "clipboard"
			u.setStatus("Output needs ≥1 destination — kept clipboard.")
			return
		}
		var parts []string
		if want["clipboard"] {
			parts = append(parts, "clipboard")
		}
		if want["paste"] {
			parts = append(parts, "paste")
		}
		cfg.OutputMode = strings.Join(parts, ",")
	})
	outGroup.SetSelected(sel)

	trimCheck := widget.NewCheck("Remove silent gaps over 1s", func(b bool) { cfg.TrimSilence = b })
	trimCheck.SetChecked(cfg.TrimSilence)
	saveCheck := widget.NewCheck("Save recording artifacts to disk", func(b bool) { cfg.SaveRecordings = b })
	saveCheck.SetChecked(cfg.SaveRecordings)
	storageEntry := widget.NewEntry()
	storageEntry.SetText(cfg.StorageDir)
	storageEntry.OnChanged = func(s string) { cfg.StorageDir = strings.TrimSpace(s) }
	muteCheck := widget.NewCheck("Mute speakers while recording", func(b bool) { cfg.MuteWhileRecording = b })
	muteCheck.SetChecked(cfg.MuteWhileRecording)
	preserveCheck := widget.NewCheck("Restore clipboard after paste", func(b bool) { cfg.PreserveClipboardAfterPaste = b })
	preserveCheck.SetChecked(cfg.PreserveClipboardAfterPaste)

	maxEntry := widget.NewEntry()
	maxEntry.SetText(strconv.Itoa(cfg.MaxTimeSeconds))
	maxEntry.OnChanged = func(s string) {
		if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && v >= 0 {
			cfg.MaxTimeSeconds = v
		}
	}
	delayEntry := widget.NewEntry()
	delayEntry.SetText(strconv.Itoa(cfg.PasteDelaySeconds))
	delayEntry.OnChanged = func(s string) {
		if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && v >= 0 {
			cfg.PasteDelaySeconds = v
		}
	}
	langEntry := widget.NewEntry()
	langEntry.SetText(cfg.Language)
	langEntry.OnChanged = func(s string) { cfg.Language = strings.TrimSpace(s) }
	outLangEntry := widget.NewEntry()
	outLangEntry.SetText(cfg.OutputLanguage)
	outLangEntry.OnChanged = func(s string) { cfg.OutputLanguage = strings.TrimSpace(s) }
	vocabEntry := widget.NewEntry()
	vocabEntry.SetText(cfg.CustomVocabulary)
	vocabEntry.OnChanged = func(s string) { cfg.CustomVocabulary = s }

	note := widget.NewLabel("Settings apply to this session. Hotkey: " + u.hotkeyHint() +
		". To persist, edit " + u.cfgPath)
	note.Wrapping = fyne.TextWrapWord

	return container.NewVBox(
		widget.NewLabelWithStyle("Audio", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewLabel("Input device"), container.NewHBox(deviceSel, refreshDev),
		widget.NewLabel("Trigger"), triggerSel,
		widget.NewLabel("Push-to-talk key (when trigger=push-to-talk)"), pttEntry,
		widget.NewLabelWithStyle("Pipeline", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewLabel("Prompt"), promptSel,
		widget.NewLabel("Cleanup model"), modelEntry,
		widget.NewLabel("Speech language (ISO-639-1, empty = auto)"), langEntry,
		widget.NewLabel("Output language (empty = same as speech)"), outLangEntry,
		widget.NewLabel("Custom vocabulary (domain spellings)"), vocabEntry,
		widget.NewLabel("Auto output destinations"), outGroup,
		widget.NewLabelWithStyle("Capture", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		trimCheck, saveCheck,
		widget.NewLabel("Storage directory"), storageEntry,
		muteCheck, preserveCheck,
		widget.NewLabel("Auto-stop after N seconds (0 = off)"), maxEntry,
		widget.NewLabel("Paste delay in seconds (0 = immediate)"), delayEntry,
		note,
	)
}

// deviceOptions returns select labels plus a label→UID map. The first entry
// is always the system default (empty UID).
func (u *uiApp) deviceOptions() ([]string, map[string]string) {
	byName := map[string]string{"System default": ""}
	names := []string{"System default"}
	devs, err := audio.ListInputDevices()
	if err != nil {
		return names, byName
	}
	for _, d := range devs {
		label := d.Name
		if _, taken := byName[label]; taken {
			label = d.Name + " (" + d.UID + ")"
		}
		byName[label] = d.UID
		names = append(names, label)
	}
	return names, byName
}

// selectDevice points sel at the live cfg.InputDevice, appending a
// "Current: <uid>" row when the device isn't in the list (unplugged).
// Needed because Select.SetSelected silently ignores unknown options.
func (u *uiApp) selectDevice(sel *widget.Select, names []string, byName map[string]string) {
	cur := u.currentDeviceName(names, byName)
	for _, n := range sel.Options {
		if n == cur {
			sel.SetSelected(cur)
			return
		}
	}
	sel.Options = append(sel.Options, cur)
	sel.SetSelected(cur)
}

// currentDeviceName maps the live cfg.InputDevice back to a select label,
// adding a "Current: <uid>" row when the UID isn't in the fresh list (e.g.
// the device is unplugged) so the dropdown never lies about what's used.
func (u *uiApp) currentDeviceName(names []string, byName map[string]string) string {
	for name, uid := range byName {
		if uid == u.cfg.InputDevice {
			return name
		}
	}
	if u.cfg.InputDevice != "" {
		label := "Current: " + u.cfg.InputDevice
		byName[label] = u.cfg.InputDevice
		return label
	}
	return names[0]
}

// ---------------------------------------------------------------------------
// recording state machine
// ---------------------------------------------------------------------------

func (u *uiApp) toggle() {
	u.mu.Lock()
	s := u.state
	u.mu.Unlock()
	switch s {
	case stIdle:
		u.startRecording()
	case stRecording:
		u.stopRecording(false)
	case stProcessing:
		// A run is in flight; ignore toggles until it lands.
	}
}

// cancelRecording abandons the in-flight capture without transcribing.
func (u *uiApp) cancelRecording() {
	u.mu.Lock()
	s := u.state
	u.mu.Unlock()
	if s == stRecording {
		u.stopRecording(true)
	}
}

func (u *uiApp) startRecording() {
	u.mu.Lock()
	if u.state != stIdle {
		u.mu.Unlock()
		return
	}
	rec, err := audio.NewRecorder(u.cfg.InputDevice)
	if err != nil {
		u.mu.Unlock()
		u.setStatus("✗ audio: " + err.Error())
		return
	}
	if err := rec.Start(); err != nil {
		u.mu.Unlock()
		u.setStatus("✗ could not start capture: " + err.Error() + " — check the mic + Settings → device.")
		return
	}
	if u.cfg.MuteWhileRecording {
		if err := mute.Mute(); err != nil {
			// Best-effort; the recording matters more than the mute.
			defer fyne.Do(func() { u.setStatus("⚠ mute failed (recording anyway): " + err.Error()) })
		}
	}
	u.state = stRecording
	u.recorder = rec
	u.recStart = time.Now()
	u.meterStop = make(chan struct{})
	if u.cfg.MaxTimeSeconds > 0 {
		d := time.Duration(u.cfg.MaxTimeSeconds) * time.Second
		u.stopTimer = time.AfterFunc(d, func() { fyne.Do(func() { u.stopRecording(false) }) })
	}
	u.mu.Unlock()

	fyne.Do(func() {
		u.recordBtn.SetText("■ Stop  (R)")
		u.setStatus("● Recording — press R, Space on the button, or " + strings.ToUpper(u.hotkey) + " to stop.")
		u.timerLbl.SetText("00:00")
		u.meter.SetText(levelBar(nil))
	})
	go u.meterLoop(u.meterStop)
}

// stopRecording ends capture. When cancelled the WAV is discarded and
// nothing is transcribed; otherwise the file fans out to Process on a
// worker goroutine so the UI never blocks on the network.
func (u *uiApp) stopRecording(cancelled bool) {
	u.mu.Lock()
	if u.state != stRecording {
		u.mu.Unlock()
		return
	}
	u.state = stProcessing
	if u.stopTimer != nil {
		u.stopTimer.Stop()
		u.stopTimer = nil
	}
	if u.meterStop != nil {
		close(u.meterStop)
		u.meterStop = nil
	}
	rec := u.recorder
	u.recorder = nil
	recDur := time.Since(u.recStart)
	u.mu.Unlock()

	wavPath, err := rec.Stop()
	_ = mute.Unmute()
	if cancelled {
		if wavPath != "" {
			_ = os.Remove(wavPath)
		}
		u.mu.Lock()
		u.state = stIdle
		u.mu.Unlock()
		fyne.Do(func() {
			u.recordBtn.SetText("● Record  (R)")
			u.setStatus("● Ready — recording discarded.")
			u.timerLbl.SetText("")
			u.meter.SetText(levelBar(nil))
		})
		return
	}
	if err != nil {
		u.backToIdle("✗ " + err.Error() + " — did the mic capture anything?")
		return
	}
	fyne.Do(func() {
		u.recordBtn.Disable()
		u.setStatus("◌ Transcribing…")
	})
	// Snapshot the config before leaving the UI thread: the Settings tab
	// mutates u.cfg live, and reading those fields from the worker
	// goroutine would race with it.
	cfg := *u.cfg
	go u.process(wavPath, recDur, cfg)
}

func (u *uiApp) backToIdle(status string) {
	u.mu.Lock()
	u.state = stIdle
	u.mu.Unlock()
	fyne.Do(func() {
		u.recordBtn.Enable()
		u.recordBtn.SetText("● Record  (R)")
		u.timerLbl.SetText("")
		u.meter.SetText(levelBar(nil))
		u.setStatus(status)
	})
}

// process runs the shared pipeline then fans results back onto the UI
// thread: text widgets, history, and the auto output destinations. cfg is a
// snapshot taken on the UI thread so Settings edits cannot race it.
func (u *uiApp) process(wavPath string, recDur time.Duration, cfg config.Config) {
	defer os.Remove(wavPath)

	systemPrompt, ok := cfg.Prompts[cfg.ActivePrompt]
	if !ok {
		u.backToIdle(fmt.Sprintf("✗ prompt %q has no body under [prompts]", cfg.ActivePrompt))
		return
	}
	systemPrompt = prompts.ApplyOutputLanguage(systemPrompt, cfg.OutputLanguage)
	systemPrompt = prompts.ApplyVocabulary(systemPrompt, cfg.CustomVocabulary)

	res, err := Process(context.Background(), wavPath, PipelineOpts{
		APIKey:             u.apiKey,
		BaseURL:            cfg.BaseURL,
		TranscriptionModel: cfg.TranscriptionModel,
		Language:           cfg.Language,
		CleanupModel:       cfg.CleanupModel,
		FallbackModel:      cfg.CleanupFallbackModel,
		SystemPrompt:       systemPrompt,
		TrimSilence:        cfg.TrimSilence,
		SaveRecordings:     cfg.SaveRecordings,
		StorageDir:         cfg.StorageDir,
	})
	if err != nil {
		// The transcript survives a cleanup failure: show it rather than
		// discarding words the user already spoke.
		if res.Raw != "" {
			u.backToIdle("⚠ " + err.Error() + " — showing the raw transcript instead")
			fyne.Do(func() { u.result.SetText(res.Raw) })
			return
		}
		u.backToIdle("✗ " + err.Error())
		return
	}
	if res.Cleaned == "" {
		u.mu.Lock()
		u.state = stIdle
		u.mu.Unlock()
		fyne.Do(func() {
			u.recordBtn.Enable()
			u.recordBtn.SetText("● Record  (R)")
			u.setStatus("⚠ (no speech detected)")
			if res.RecordingDir != "" {
				u.savedLbl.SetText("Saved in " + res.RecordingDir)
			}
		})
		return
	}

	u.mu.Lock()
	u.state = stIdle
	u.sessions = append([]session{{
		when:    time.Now(),
		raw:     res.Raw,
		cleaned: res.Cleaned,
		dir:     res.RecordingDir,
	}}, u.sessions...)
	if len(u.sessions) > 100 {
		u.sessions = u.sessions[:100]
	}
	_, clipOn, pasteOn := cfg.OutputDestinations()
	preserve := cfg.PreserveClipboardAfterPaste
	delay := cfg.PasteDelaySeconds
	u.mu.Unlock()

	fyne.Do(func() {
		u.recordBtn.Enable()
		u.recordBtn.SetText("● Record  (R)")
		u.result.SetText(res.Cleaned)
		u.setRaw(res.Raw)
		if res.RecordingDir != "" {
			u.savedLbl.SetText("Saved in " + res.RecordingDir)
		} else {
			u.savedLbl.SetText("")
		}
		u.histList.Refresh()
	})
	go u.autoOutput(res.Cleaned, recDur, res, clipOn, pasteOn, preserve, delay)
}

// autoOutput mirrors the CLI's Done line: optional paste-delay countdown
// ("focus the target window"), then output.Write with stdout disabled —
// the window itself IS the stdout.
func (u *uiApp) autoOutput(text string, recDur time.Duration, res PipelineResult, clipOn, pasteOn bool, preserve bool, delay int) {
	if !clipOn && !pasteOn {
		fyne.Do(func() {
			u.setStatus(fmt.Sprintf("✓ Done — %s (auto output off; use Copy/Paste)", shortStats(text, recDur, res.ProcessTime)))
		})
		return
	}
	if pasteOn && delay > 0 {
		for i := delay; i > 0; i-- {
			fyne.Do(func() {
				u.setStatus(fmt.Sprintf("Pasting in %ds — focus the destination window…", i))
			})
			time.Sleep(time.Second)
		}
	}
	_, err := output.Write(text, output.Destinations{
		Clipboard:              clipOn,
		Paste:                  pasteOn,
		PreservePriorClipboard: preserve,
		PasteDelay:             0, // countdown already handled above
	})
	stats := shortStats(text, recDur, res.ProcessTime)
	fyne.Do(func() {
		if err != nil {
			u.setStatus("⚠ Done (" + stats + ") but output failed: " + err.Error())
			return
		}
		var got []string
		if clipOn {
			got = append(got, "copied to clipboard")
		}
		if pasteOn {
			got = append(got, "pasted")
		}
		u.setStatus("✓ Done — " + stats + " · " + strings.Join(got, " · "))
	})
}

func shortStats(text string, rec, proc time.Duration) string {
	words := len(strings.Fields(text))
	return fmt.Sprintf("%d chars · %d words · rec %s · proc %s",
		len(text), words, roundDur(rec), roundDur(proc))
}

func roundDur(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// ---------------------------------------------------------------------------
// buttons, meter, history
// ---------------------------------------------------------------------------

func (u *uiApp) copyNow() {
	text := u.result.Text
	if strings.TrimSpace(text) == "" {
		u.setStatus("Nothing to copy.")
		return
	}
	go func() {
		if _, err := output.Write(text, output.Destinations{Clipboard: true}); err != nil {
			fyne.Do(func() { u.setStatus("✗ copy failed: " + err.Error()) })
			return
		}
		fyne.Do(func() { u.setStatus("Copied to clipboard.") })
	}()
}

func (u *uiApp) pasteNow() {
	text := u.result.Text
	if strings.TrimSpace(text) == "" {
		u.setStatus("Nothing to paste.")
		return
	}
	seconds := u.cfg.PasteDelaySeconds
	preserve := u.cfg.PreserveClipboardAfterPaste
	go func() {
		if seconds > 0 {
			for i := seconds; i > 0; i-- {
				fyne.Do(func() {
					u.setStatus(fmt.Sprintf("Pasting in %ds — focus the destination window…", i))
				})
				time.Sleep(time.Second)
			}
		}
		if _, err := output.Write(text, output.Destinations{
			Paste:                  true,
			PreservePriorClipboard: preserve,
		}); err != nil {
			fyne.Do(func() { u.setStatus("✗ paste failed: " + err.Error()) })
			return
		}
		fyne.Do(func() { u.setStatus("✓ Pasted into the focused app.") })
	}()
}

// meterLoop repaints the level bar + elapsed timer ~10×/s from the live
// recorder peaks until stop is closed.
func (u *uiApp) meterLoop(stop <-chan struct{}) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			u.mu.Lock()
			rec := u.recorder
			start := u.recStart
			u.mu.Unlock()
			if rec == nil {
				return
			}
			bar := levelBar(rec.PeakHistory())
			elapsed := time.Since(start).Truncate(time.Second)
			mm := int(elapsed.Minutes())
			ss := int(elapsed.Seconds()) % 60
			fyne.Do(func() {
				u.meter.SetText(bar)
				u.timerLbl.SetText(fmt.Sprintf("%02d:%02d", mm, ss))
			})
		}
	}
}

// refreshHistory seeds the History tab from the artifact directories on
// disk (best-effort: a missing/unreadable storage root just leaves the
// session entries).
func (u *uiApp) refreshHistory() {
	// Snapshot off the UI thread's data before spawning: Settings may edit
	// StorageDir at any time.
	storageDir := u.cfg.StorageDir
	go func() {
		items := RecentRecordings(storageDir, 30)
		fyne.Do(func() {
			u.mu.Lock()
			keep := u.sessions[:0]
			for _, s := range u.sessions {
				if s.dir == "" {
					keep = append(keep, s)
				}
			}
			seen := map[string]bool{}
			for _, s := range keep {
				seen[s.dir] = true
			}
			for _, it := range items {
				if it.Dir == "" || seen[it.Dir] {
					continue
				}
				raw, cleaned := readSession(it.Dir)
				keep = append(keep, session{when: it.Time, raw: raw, cleaned: cleaned, dir: it.Dir})
				seen[it.Dir] = true
			}
			u.sessions = keep
			u.mu.Unlock()
			u.histList.Refresh()
		})
	}()
}

// readSession loads a past artifact directory for History selection.
func readSession(dir string) (raw, cleaned string) {
	if b, err := os.ReadFile(dir + "/raw.txt"); err == nil {
		raw = string(b)
	}
	if b, err := os.ReadFile(dir + "/cleaned.txt"); err == nil {
		cleaned = string(b)
	}
	return raw, cleaned
}

func (u *uiApp) setStatus(s string) { u.status.SetText(s) }

func (u *uiApp) setRaw(raw string) {
	if strings.TrimSpace(raw) == "" {
		u.rawLbl.SetText("")
		return
	}
	u.rawLbl.SetText("Raw: " + previewText(raw))
}

// ---------------------------------------------------------------------------
// global hotkeys (gohook) — system-wide toggle + PTT hold
// ---------------------------------------------------------------------------

func normalizeKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// holdEnabled reports whether the CLI-style hold-to-record gesture is
// armed: trigger=push-to-talk with a PTT key distinct from the toggle
// hotkey (same key would double-drive the state machine). pttKey is passed
// in so the caller snapshots config on its own thread.
func (u *uiApp) holdEnabled(pttKey string) bool {
	return pttKey != "" && pttKey != u.hotkey
}

// watchHotkeys owns the single process-wide gohook pump for the app's
// lifetime. Toggle (hotkey KeyDown) and hold (ptt KeyDown/KeyUp) share one
// hook.Start so they never fight over the global listener. On hosts where
// the hook sees no events (Wayland without XWayland grabs, missing
// permissions) this goroutine simply never fires — the window controls
// keep working and the footer says as much.
func (u *uiApp) watchHotkeys(ctx context.Context) {
	// Snapshot the hold key once: this runs off the UI thread, which owns
	// the config writes from the Settings tab.
	pttKey := ""
	if u.cfg.Trigger == "push-to-talk" {
		pttKey = normalizeKey(u.cfg.PTTKey)
	}
	if u.hotkey == "" && !u.holdEnabled(pttKey) {
		return
	}
	toggleCh := make(chan struct{}, 4)
	var downCh, upCh chan struct{}
	// On Wayland the X11 hook only sees keys while our own window has
	// focus, so it cannot be the global hotkey. Worse, if the desktop
	// shortcut (which does work globally) is also bound to the same key,
	// both would fire and toggle twice. The desktop owns the toggle key
	// there; keep the hook for push-to-talk only.
	if u.hotkey != "" && !OnWayland() {
		k := u.hotkey
		hook.Register(hook.KeyDown, []string{k}, func(hook.Event) {
			select {
			case toggleCh <- struct{}{}:
			default:
			}
		})
	}
	if u.holdEnabled(pttKey) {
		downCh = make(chan struct{}, 4)
		upCh = make(chan struct{}, 4)
		p := pttKey
		hook.Register(hook.KeyDown, []string{p}, func(hook.Event) {
			select {
			case downCh <- struct{}{}:
			default:
			}
		})
		hook.Register(hook.KeyUp, []string{p}, func(hook.Event) {
			select {
			case upCh <- struct{}{}:
			default:
			}
		})
	}

	ev := hook.Start()
	go hook.Process(ev)
	defer hook.End()

	for {
		select {
		case <-ctx.Done():
			return
		case <-toggleCh:
			fyne.Do(func() { u.toggle() })
		case <-downCh:
			fyne.Do(func() { u.holdStart() })
		case <-upCh:
			fyne.Do(func() { u.holdStop() })
		}
	}
}

// holdStart begins a PTT-owned recording; holdStop ends it. The held flag
// keeps a stray toggle from stealing the session mid-hold.
func (u *uiApp) holdStart() {
	u.mu.Lock()
	idle := u.state == stIdle
	if idle {
		u.held = true
	}
	u.mu.Unlock()
	if idle {
		u.startRecording()
	}
}

func (u *uiApp) holdStop() {
	u.mu.Lock()
	owned := u.held && u.state == stRecording
	u.held = false
	u.mu.Unlock()
	if owned {
		u.stopRecording(false)
	}
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

var meterGlyphs = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// levelBar renders one 7-cell block meter from recorder peaks, the same
// vocabulary as the CLI's stderr meter so the two surfaces feel related.
func levelBar(peaks []float64) string {
	var b strings.Builder
	for i := 0; i < 7; i++ {
		var l float64
		if i < len(peaks) {
			l = peaks[i]
		}
		b.WriteRune(meterGlyph(l))
	}
	return b.String()
}

func meterGlyph(level float64) rune {
	if level <= 0 {
		return meterGlyphs[0]
	}
	if level >= 1 {
		return meterGlyphs[len(meterGlyphs)-1]
	}
	idx := int(level * float64(len(meterGlyphs)))
	if idx >= len(meterGlyphs) {
		idx = len(meterGlyphs) - 1
	}
	return meterGlyphs[idx]
}

// openPath reveals a directory in the platform file manager.
func openPath(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", path).Start()
	default:
		if _, err := exec.LookPath("xdg-open"); err == nil {
			return exec.Command("xdg-open", path).Start()
		}
		return fmt.Errorf("no opener found (install xdg-open)")
	}
}
