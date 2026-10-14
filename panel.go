package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"
)

const (
	defaultPanelAddr = "127.0.0.1:47821"
	maxNotesBytes    = 200 * 1024
)

//go:embed web/index.html
var indexHTML []byte

//go:embed web/logo.svg
var logoSVG []byte

const (
	endedKeepMs  = 24 * 60 * 60 * 1000 // 結束超過一天的對話自動不顯示
	staleWorking = 15 * 60 * 1000      // 「執行中」但記錄 15 分鐘沒動，當成閒置（工具當掉沒送 Stop）
)

type row struct {
	Key       string `json:"key"`
	Agent     string `json:"agent"`
	Project   string `json:"project"`
	Status    string `json:"status"`
	UpdatedAt int64  `json:"updatedAt"`
	Stats
}

type panel struct {
	mu      sync.Mutex
	parsers map[string]transcriptParser // 面板自己的進度，不跟 hook 搶檔案
}

func runPanel() {
	addr := panelAddress()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// 已經有一個面板在跑：只把視窗叫出來
		openWindow("http://" + addr + "/")
		return
	}
	p := &panel{parsers: map[string]transcriptParser{}}
	url := "http://" + addr + "/"
	if addr == "127.0.0.1:0" || addr == "localhost:0" {
		url = "http://" + ln.Addr().String() + "/"
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		openWindow(url)
	}()
	_ = http.Serve(ln, panelHandler(p))
}

func panelAddress() string {
	if addr := os.Getenv("AIMON_PANEL_ADDR"); addr != "" {
		return addr
	}
	return defaultPanelAddr
}

func panelHandler(p *panel) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})
	mux.HandleFunc("/logo.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(logoSVG)
	})
	mux.HandleFunc("/api/sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"now": nowMs(), "rows": p.rows()})
	})
	// 沙箱裡的 hook 寫不了檔，把事件送來這裡代辦（見 hook.go 的 delegateToPanel）
	mux.HandleFunc("/api/hook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		agent := r.URL.Query().Get("agent")
		if agent != "claude" && agent != "codex" {
			http.Error(w, "unknown agent", http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		runHook(agent, io.LimitReader(r.Body, 1<<20), w)
	})
	mux.HandleFunc("/api/hide", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		if s, ok := loadSession(r.URL.Query().Get("key")); ok {
			s.HiddenAt = nowMs()
			_ = saveSession(s)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/notes", notesHandler)
	return mux
}

func notesHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	path := filepath.Join(dataDir(), "notes.md")
	switch r.Method {
	case http.MethodGet:
		b, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			http.Error(w, "讀取筆記失敗", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(b)
	case http.MethodPut:
		b, err := io.ReadAll(io.LimitReader(r.Body, maxNotesBytes+1))
		if err != nil {
			http.Error(w, "讀取內容失敗", http.StatusBadRequest)
			return
		}
		if len(b) > maxNotesBytes {
			http.Error(w, "筆記不能超過 200KB", http.StatusRequestEntityTooLarge)
			return
		}
		if err := writeNotes(path, b); err != nil {
			http.Error(w, "儲存筆記失敗", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "GET or PUT only", http.StatusMethodNotAllowed)
	}
}

// writeNotes 先寫同目錄暫存檔再改名，避免斷電或中斷時留下半份筆記。
func writeNotes(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "notes-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (p *panel) rows() []row {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := nowMs()
	out := []row{}
	for _, s := range listSessions() {
		if s.HiddenAt != 0 || s.Status == "ended" && now-s.UpdatedAt > endedKeepMs {
			continue
		}
		pr := p.parsers[s.Key()]
		if pr == nil {
			pr = newParser(s.Agent)
			p.parsers[s.Key()] = pr
		}
		if s.Transcript != "" {
			_ = pr.Update(s.Transcript)
		}
		st := pr.Stats()
		status := s.Status
		if status == "working" {
			if fi, err := os.Stat(s.Transcript); err == nil && now-fi.ModTime().UnixMilli() > staleWorking {
				status = "idle"
			}
		}
		out = append(out, row{Key: s.Key(), Agent: s.Agent, Project: s.Project(), Status: status, UpdatedAt: s.UpdatedAt, Stats: st})
	}
	// 執行中的在上面，其餘依最近動靜排序
	sort.SliceStable(out, func(i, j int) bool {
		wi, wj := out[i].Status == "working", out[j].Status == "working"
		if wi != wj {
			return wi
		}
		return max(out[i].UpdatedAt, out[i].LastAt) > max(out[j].UpdatedAt, out[j].LastAt)
	})
	return out
}

// openWindow：用 Chrome／Edge 的 --app 開一個沒有網址列的小視窗；都找不到就用預設瀏覽器
func openWindow(url string) {
	app := []string{"--app=" + url, "--window-size=520,640"}
	switch runtime.GOOS {
	case "windows":
		for _, exe := range []string{
			os.Getenv("ProgramFiles") + `\Google\Chrome\Application\chrome.exe`,
			os.Getenv("ProgramFiles(x86)") + `\Google\Chrome\Application\chrome.exe`,
			os.Getenv("LocalAppData") + `\Google\Chrome\Application\chrome.exe`,
			os.Getenv("ProgramFiles(x86)") + `\Microsoft\Edge\Application\msedge.exe`,
			os.Getenv("ProgramFiles") + `\Microsoft\Edge\Application\msedge.exe`,
		} {
			if _, err := os.Stat(exe); err == nil {
				if exec.Command(exe, app...).Start() == nil {
					return
				}
			}
		}
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		for _, name := range []string{"Google Chrome", "Microsoft Edge"} {
			if exec.Command("open", append([]string{"-na", name, "--args"}, app...)...).Run() == nil {
				return
			}
		}
		_ = exec.Command("open", url).Start()
	default:
		_ = exec.Command("xdg-open", url).Start()
	}
	fmt.Fprintln(os.Stderr, "面板：", url)
}
