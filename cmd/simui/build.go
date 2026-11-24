package main

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

// buildDrawerState is the optional Source/Build drawer. The source editor is a
// single file (the common case for the bundled examples); the build command is
// a shell command run in a chosen directory. Processes and their output are
// owned here so the UI frame only reads snapshots.
type buildDrawerState struct {
	mu sync.Mutex

	tab        int
	sourcePath string
	doc        TextDocument
	saved      string
	docErr     string
	dirty      bool

	buildDir string
	buildCmd string
	log      *TextRing
	running  bool
	status   string
	cancel   context.CancelFunc
}

var (
	buildPanel     *buildDrawerState
	buildPanelOpen bool
)

func newBuildDrawerState() *buildDrawerState {
	return &buildDrawerState{log: NewTextRing(128 * 1024)}
}

// initBuildDrawer fills the drawer defaults. Empty explicit values are inferred
// from the firmware path, so `--watch` + the drawer works without extra flags
// for the bundled TinyGo and C examples.
func initBuildDrawer(dir, cmd, source string) {
	if buildPanel != nil {
		return
	}
	bp := newBuildDrawerState()
	inferDir, inferCmd, inferSource := inferBuildPlan(fwPath)
	if dir == "" {
		dir = inferDir
	}
	if cmd == "" {
		cmd = inferCmd
	}
	if dir != inferDir && cmd == "" {
		cmd = inferCommand(dir, fwPath)
	}
	if source == "" {
		source = inferSource
	}
	bp.buildDir, bp.buildCmd = dir, cmd
	if source != "" {
		bp.loadSource(source)
	}
	buildPanel = bp
}

// inferBuildPlan guesses the project directory, command and source file for a
// firmware path. The bundled layout is build/foo.elf for TinyGo and
// firmware/c/build/foo.elf for the Pico SDK examples.
func inferBuildPlan(fw string) (dir, cmd, source string) {
	if fw == "" {
		wd, _ := os.Getwd()
		return wd, "", ""
	}
	abs, err := filepath.Abs(fw)
	if err != nil {
		abs = fw
	}
	fwDir := filepath.Dir(abs)
	stem := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))

	tinyProject := func(dir string) (string, string, string) {
		src := filepath.Join(dir, "main.go")
		if !fileExists(src) {
			src = ""
		}
		return dir, tinygoCommand(abs), src
	}
	cProject := func(project, buildDir string) (string, string, string) {
		return project, cmakeCommand(buildDir), ""
	}
	if filepath.Base(fwDir) == "build" {
		root := filepath.Dir(fwDir)
		// <root>/build/<stem>.elf -> <root>/firmware/<stem>
		if tiny := filepath.Join(root, "firmware", stem); fileExists(filepath.Join(tiny, "go.mod")) {
			return tinyProject(tiny)
		}
		// <root>/build/<stem>.elf -> <root>/firmware/c
		if cdir := filepath.Join(root, "firmware", "c"); fileExists(filepath.Join(cdir, "CMakeLists.txt")) {
			return cProject(cdir, fwDir)
		}
		// <project>/build/<stem>.elf with CMakeLists.txt in the project.
		if project := root; fileExists(filepath.Join(project, "CMakeLists.txt")) {
			return cProject(project, fwDir)
		}
	}
	// Walk up for an in-source or non-standard build directory. The first
	// go.mod/CMakeLists.txt above the ELF owns the project.
	for d := fwDir; d != ""; {
		if fileExists(filepath.Join(d, "go.mod")) {
			return tinyProject(d)
		}
		if fileExists(filepath.Join(d, "CMakeLists.txt")) {
			buildDir := fwDir
			if buildDir == d {
				buildDir = filepath.Join(d, "build")
			}
			return cProject(d, buildDir)
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return fwDir, "", ""
}

// inferCommand builds a default command for an explicitly chosen directory.
func inferCommand(dir, fw string) string {
	if fileExists(filepath.Join(dir, "go.mod")) {
		out := fw
		if out == "" {
			out = filepath.Join(dir, "firmware.elf")
		}
		return tinygoCommand(out)
	}
	if fileExists(filepath.Join(dir, "CMakeLists.txt")) {
		return cmakeCommand(filepath.Join(dir, "build"))
	}
	return ""
}

func tinygoCommand(out string) string {
	return "tinygo build -target=pico2 -o " + shellQuote(out) + " ."
}

func cmakeCommand(buildDir string) string {
	return "cmake --build " + shellQuote(buildDir)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// shellQuote quotes one path for the platform shell used by StartBuild.
func shellQuote(s string) string {
	if runtime.GOOS == "windows" {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shellCommand(ctx context.Context, line string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", line)
	}
	return exec.CommandContext(ctx, "sh", "-c", line)
}

// loadSource reads path into the editor. It is called from the UI thread when
// the drawer is first opened or the user presses Open.
func (b *buildDrawerState) loadSource(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		b.docErr = "choose a source file"
		return
	}
	b.sourcePath = path
	data, err := os.ReadFile(path)
	if err != nil {
		b.docErr = err.Error()
		return
	}
	b.doc.SetText(string(data))
	b.saved = string(data)
	b.dirty = false
	b.docErr = ""
}

func (b *buildDrawerState) saveSource() {
	path := strings.TrimSpace(b.sourcePath)
	if path == "" {
		b.docErr = "choose a source file"
		return
	}
	text := b.doc.Text()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		b.docErr = err.Error()
		return
	}
	b.saved = text
	b.dirty = false
	b.docErr = ""
}

func (b *buildDrawerState) append(line string) {
	b.mu.Lock()
	b.log.AppendLine(line)
	b.mu.Unlock()
	RequestNextFrame()
}

func (b *buildDrawerState) setStatus(s string) {
	b.mu.Lock()
	b.status = s
	b.mu.Unlock()
	RequestNextFrame()
}

// StartBuild runs the configured command in a background goroutine.
func (b *buildDrawerState) StartBuild() {
	b.mu.Lock()
	if b.running {
		b.mu.Unlock()
		return
	}
	line := strings.TrimSpace(b.buildCmd)
	dir := strings.TrimSpace(b.buildDir)
	if line == "" {
		b.mu.Unlock()
		b.setStatus("no build command: choose a directory or enter one")
		return
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}
	b.running = true
	b.status = "building…"
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.mu.Unlock()

	b.append("$ " + line)
	go func() {
		defer cancel()
		cmd := shellCommand(ctx, line)
		cmd.Dir = dir
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			b.finish(err)
			return
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			b.finish(err)
			return
		}
		if err := cmd.Start(); err != nil {
			b.finish(err)
			return
		}
		var wg sync.WaitGroup
		scan := func(r interface{ Read([]byte) (int, error) }) {
			defer wg.Done()
			sc := bufio.NewScanner(r)
			sc.Buffer(make([]byte, 64*1024), 1024*1024)
			for sc.Scan() {
				b.append(sc.Text())
			}
		}
		wg.Add(2)
		go scan(stdout)
		go scan(stderr)
		wg.Wait()
		b.finish(cmd.Wait())
	}()
}

func (b *buildDrawerState) finish(err error) {
	b.mu.Lock()
	b.running = false
	b.cancel = nil
	if err != nil {
		b.status = "build failed: " + err.Error()
	} else {
		b.status = "build succeeded"
	}
	b.mu.Unlock()
	RequestNextFrame()
}

// StopBuild cancels a running build.
func (b *buildDrawerState) StopBuild() {
	b.mu.Lock()
	cancel := b.cancel
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (b *buildDrawerState) snapshot() (running bool, status string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.running, b.status
}

func (b *buildDrawerState) renderLog() {
	b.mu.Lock()
	defer b.mu.Unlock()
	LogView(b.log, TextStyle(Fonts(Monospace...), FontSize(11), TextColor(140, 12, 85, 1)))
}

// buildDrawer renders the right-hand Source/Build panel.
func buildDrawer(snap MachineState) {
	bp := buildPanel
	if bp == nil {
		return
	}
	Container(Attrs(FixWidth(470), Expand, Gap(8), Pad(10), Background(220, 10, 15, 1),
		BorderWidth(1), BorderColor(220, 20, 30, 1)), func() {
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label("Source / Build", FontSize(16), FontWeight(700))
			Filler(1)
			if Button(NoIcon, "Close") {
				buildPanelOpen = false
				RequestNextFrame()
			}
		})
		Container(Attrs(Row, Gap(4)), func() {
			drawerTab("Source", 0, bp)
			drawerTab("Build", 1, bp)
		})
		if bp.tab == 1 {
			buildTab(bp)
		} else {
			sourceTab(bp)
		}
	})
}

func drawerTab(label string, tab int, bp *buildDrawerState) {
	Container(Attrs(Row, CrossMid, Pad2(4, 12), Corners(4)), func() {
		st := ProcessButtonEvents(false)
		if st.Clicked {
			bp.tab = tab
			RequestNextFrame()
		}
		switch {
		case bp.tab == tab:
			ModAttrs(Background(220, 14, 26, 1))
		case st.Hovered:
			ModAttrs(Background(220, 12, 21, 1))
		}
		Label(label, FontSize(12), FontWeight(600))
	})
}

func sourceTab(bp *buildDrawerState) {
	attrs := DefaultTextInputAttrs()
	attrs.MinWidth = 290
	attrs.FixedWidth = true
	Container(Attrs(Grow(1), Expand, Gap(6)), func() {
		Label("File", FontSize(11), FontWeight(600), TextColor(220, 5, 62, 1))
		Container(Attrs(Row, Gap(6), CrossMid), func() {
			TextInputExt(&bp.sourcePath, attrs)
			if Button(NoIcon, "Open") {
				bp.loadSource(bp.sourcePath)
				RequestNextFrame()
			}
			if Button(NoIcon, "Save") {
				bp.saveSource()
				RequestNextFrame()
			}
		})
		if bp.docErr != "" {
			Label(bp.docErr, FontSize(11), TextColor(0, 65, 60, 1))
		} else if bp.dirty {
			Label("unsaved changes", FontSize(11), FontWeight(600), TextColor(140, 45, 68, 1))
		} else if bp.sourcePath != "" {
			Label("saved", FontSize(11), TextColor(140, 45, 68, 1))
		}
		Container(Attrs(Grow(1), Expand, Clip, Background(220, 12, 8, 1), Corners(6), Pad(6),
			BorderWidth(1), BorderColor(220, 25, 30, 1)), func() {
			st := TextEditor(&bp.doc, FontSize(12))
			if st.Changed {
				bp.dirty = bp.doc.Text() != bp.saved
			}
		})
		Label("Save, then build. Enable --watch to reload the built image automatically.", FontSize(10), TextColor(220, 6, 58, 1))
	})
}

func buildTab(bp *buildDrawerState) {
	dirAttrs := DefaultTextInputAttrs()
	dirAttrs.MinWidth = 420
	dirAttrs.FixedWidth = true
	cmdAttrs := DefaultTextInputAttrs()
	cmdAttrs.MinWidth = 420
	cmdAttrs.FixedWidth = true
	running, status := bp.snapshot()
	Container(Attrs(Grow(1), Expand, Gap(6)), func() {
		Label("Directory", FontSize(11), FontWeight(600), TextColor(220, 5, 62, 1))
		TextInputExt(&bp.buildDir, dirAttrs)
		Label("Command", FontSize(11), FontWeight(600), TextColor(220, 5, 62, 1))
		TextInputExt(&bp.buildCmd, cmdAttrs)
		Container(Attrs(Row, Gap(6), CrossMid), func() {
			if Button(NoIcon, "Build") && !running {
				bp.StartBuild()
				RequestNextFrame()
			}
			if Button(NoIcon, "Stop") && running {
				bp.StopBuild()
				RequestNextFrame()
			}
			if Button(NoIcon, "Reload firmware") && strings.TrimSpace(fwPath) != "" {
				if flashMode {
					_ = emu.ReloadFlash(fwPath)
				} else {
					_ = emu.Reload(fwPath)
				}
				RequestNextFrame()
			}
			Filler(1)
			if running {
				Label("running", FontSize(11), FontWeight(600), TextColor(140, 50, 70, 1))
			}
		})
		if status != "" {
			Label(status, FontSize(11), TextColor(140, 45, 68, 1))
		}
		Container(Attrs(Grow(1), Expand, Clip, Background(220, 12, 8, 1), Corners(6), Pad(6),
			BorderWidth(1), BorderColor(220, 25, 30, 1)), func() {
			bp.renderLog()
		})
		Label("Uses the host shell. The default writes the loaded firmware path.", FontSize(10), TextColor(220, 6, 58, 1))
	})
}
