# AI 對話監看（aimon）

讓同仁看得到「每個 Claude Code／Codex 對話有多長、每句花多少」，越過門檻時在對話裡主動提醒。
起因：有人一個對話用到底，快取累積到 8 億 token 還不知道每句都很貴——問題在用法與認知，不在工具。

## 有什麼

- **桌面面板**：一個沒有網址列的 520×640 小視窗，有「對話／Tips／筆記」三個分頁。對話一開始就自動加進清單；按 ✕ 移除，之後有新動靜會再出現。Tips 可快速查指令、模型、思考強度、怎麼看卡片與省錢習慣；筆記在停止輸入 0.5 秒後自動存到這台電腦的 `<設定資料夾>/ai-session-monitor/notes.md`。

  ```
  ● Claude [專案資料夾]: 對話標題                   ✕
    opus-5-5 · high                           執行中
  對話 212.3K/1M (21%)
  Cache: 21.2K | 58min | $0.45 | Total $3.10
  ```

  模型與思考強度會從對話記錄取最新值；舊記錄讀不到時就不顯示。左上角的對話氣泡＋三段量表是 aimon 的離線 SVG logo，也是 Chrome／Edge app 視窗圖示。

- **💸 提醒**：對話超過 20 萬、50 萬 token，一句超過 $0.50，或快取保證時間過後才回來接著問時，在對話裡跳一行提醒，建議 /clear 或 /compact。門檻在 [hook.go](hook.go) 開頭。
- **導覽 skill**：同仁問「面板的數字是什麼」「什麼時候該 /clear」「模型和思考強度怎麼選」時，AI 會照 [skill/SKILL.md](skill/SKILL.md) 回答。

金額是 API 牌價換算（[pricing.go](pricing.go)），訂閱方案不是照這個扣錢，但它反映真實用量。

## 安裝（每位同仁做一次）

從 [Releases](https://github.com/youllook/ai-session-monitor/releases/latest) 下載對應的檔案（自己編譯的話在 `dist/`）：

| 電腦 | 檔案 |
|---|---|
| Windows | `aimon-windows-amd64.exe` |
| Mac（M 系列晶片） | `aimon-darwin-arm64` |

只出這兩個主流版本。真的遇到 ARM 的 Windows 或 Intel 的 Mac，把下面編譯指令的清單加上 `windows/arm64` 或 `darwin/amd64` 即可。

**Windows**：在終端機執行 `aimon-windows-amd64.exe install claude` 或 `aimon-windows-amd64.exe install codex`。SmartScreen 跳警告的話按「其他資訊 → 仍要執行」。為了相容舊用法，不寫 agent 的 `install` 仍等於 `install claude`。

**Mac**：這個程式沒有 Apple 簽章，第一次要先放行：

```bash
chmod +x aimon-darwin-arm64
xattr -d com.apple.quarantine aimon-darwin-arm64
./aimon-darwin-arm64 install claude   # 或 install codex
```

安裝會做三件事：
1. 把程式複製到固定位置：Windows 是 `%AppData%\ai-session-monitor\bin\`，Mac 是 `~/Library/Application Support/ai-session-monitor/bin/`。
2. 加 4 個 hook：SessionStart、UserPromptSubmit、Stop、SessionEnd。Claude 改 `~/.claude/settings.json`；Codex 改獨立的 `~/.codex/hooks.json`，**不改 `config.toml` 與既有 `notify`**。已存在的設定原檔先備份成 `*.aimon-bak`，別人的 hook 保留，重複安裝不會疊兩份。
3. 放導覽 skill 到對應的 `~/.claude/skills/ai-session-monitor/` 或 `~/.codex/skills/ai-session-monitor/`。

Codex 的使用者 hook 安裝後預設還沒被信任。第一次一般使用前，請開 Codex 執行 `/hooks`，檢視並信任 aimon 的四個 hook；自動化驗收可在已審閱 hook 後用 `--dangerously-bypass-hook-trust`。

之後開 Claude Code 對話時，面板會自動跳出來。關掉了想再開：`aimon panel`。不想自動跳出：設定環境變數 `AIMON_NO_PANEL=1`。

面板預設只監聽 `127.0.0.1:47821`。開發、驗收或預設埠已被舊面板佔用時，可先設 `AIMON_PANEL_ADDR=127.0.0.1:47831` 再執行 `aimon panel`；未設時行為不變。

移除：`aimon uninstall claude` 或 `aimon uninstall codex`。程式和紀錄留在上面那個資料夾，不要了可以整個刪掉。

### Codex 數字的限制

- 目前以 Codex CLI 0.153.0 的 rollout JSONL 為準；官方將 `transcript_path` 指向的格式列為不穩定介面，Codex 升級後要再跑解析器測試。
- GPT-5.6 以後的快取是「最近寫入或命中後至少 30 分鐘」，OpenAI 可能保留更久；面板倒數顯示這個保證時間，不代表逾時必定沒有快取。
- Codex 的 `systemMessage` 在 UI／事件串流裡是 warning，不是助理回覆內文。`SessionEnd` 可能要到關閉或對話無人連線 30 分鐘後才觸發。

## 開發

需要 Go 1.27 以上。

```bash
go test ./...
go vet ./...
go build -o aimon.exe .
./aimon.exe stats <某個對話記錄.jsonl>   # 驗證：印出面板會顯示的數字
./aimon.exe stats --agent codex <rollout.jsonl>
```

產出發佈檔（CGO 關掉，在 Windows 上就能編出 Mac 版）：

```bash
for t in windows/amd64 darwin/arm64; do
  os=${t%/*}; arch=${t#*/}; ext=""; [ $os = windows ] && ext=.exe
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w" -o dist/aimon-$os-$arch$ext .
done
```

| 檔案 | 做什麼 |
|---|---|
| `main.go` | 指令入口 |
| `hook.go` | hook 收事件：更新狀態、算帳、決定要不要提醒 |
| `claude.go` | 增量讀 Claude Code 對話記錄（JSONL），算出 Stats |
| `codex.go` | 增量讀 Codex rollout JSONL，依 response id 去重並算出 Stats |
| `pricing.go` | 單價表與上下文上限 |
| `store.go` | 每個對話一個狀態檔（`<設定資料夾>/ai-session-monitor/sessions/`） |
| `panel.go` | 本機小伺服器，提供面板、對話 API、logo 與筆記 API |
| `web/index.html` | 離線面板畫面（編譯時嵌進程式） |
| `web/logo.svg` | 向量 logo 與 favicon（編譯時嵌進程式） |
| `install.go` | 安裝／移除 Claude settings.json 或 Codex hooks.json；只動自己的 hook |
| `skill/SKILL.md` | 導覽 skill（編譯時嵌進程式） |

Codex 掛勾點、rollout 格式與查證紀錄見 [CODEX.md](CODEX.md)。
