# Codex 接軌查證與維護紀錄

這份文件記錄 aimon 0.2.0 對 Codex 的實際依據與限制。查證環境是 Windows 10、Codex CLI 0.153.0，日期 2026-10-14。Codex 升級後要重跑本頁底下的驗收，不要假設 rollout 格式永遠不變。

## 掛勾點

Codex 0.153.0 已有 lifecycle hooks，不需要拿只有「回合完成」的 `notify` 硬湊狀態。aimon 安裝這四個使用者層級 hook：

| Codex event | aimon 狀態／動作 |
|---|---|
| `SessionStart` | `idle`，確保面板已開 |
| `UserPromptSubmit` | `working`，檢查快取保證時間 |
| `Stop` | `idle`，增量讀記錄、判斷提醒 |
| `SessionEnd` | `ended` |

四個 event 都透過 stdin 傳一個 JSON object，aimon 用到的 common fields 是：

```json
{
  "session_id": "01a...",
  "transcript_path": "C:/.../.codex/sessions/.../rollout-....jsonl",
  "cwd": "D:/work/project",
  "hook_event_name": "Stop",
  "model": "gpt-5.6-sol"
}
```

`UserPromptSubmit` 和 `Stop` 還有 `turn_id`。`Stop` 的 stdout 必須是 JSON；共用輸出欄位 `systemMessage` 會被 Codex 顯示為 UI／事件串流的 warning，所以 aimon 的 💸 提醒可以顯示給使用者，但它不是助理回覆內文。

`notify = [...]` 只支援 `agent-turn-complete`，這台電腦也已有別的 `notify`。aimon 改獨立的 `~/.codex/hooks.json`，不會覆寫 `config.toml` 或 `notify`。非管理式 hook 第一次會被略過，使用者要在 Codex 輸入 `/hooks` 檢視並信任；已在外部審閱的自動化驗收可用 `--dangerously-bypass-hook-trust`。

## rollout JSONL 格式

本機 `~/.codex/sessions/` 的 0.153.0 實際檔案有這些記錄：

- `session_meta.payload`：`id`、`cwd`、`cli_version`、`source`、`model_provider`。
- `turn_context.payload`：`turn_id`、`cwd`、`model`、`effort` 等當回合設定。
- `event_msg.payload.type = "task_started"`：使用者新送出的一句。工具結果不會產生它，所以 aimon 在這裡重設 `TurnCost`。
- `response_item.payload.type = "message"` 且 `role = "user"`：可取第一個真實使用者 `input_text` 當標題。開頭是 `<environment_context>` 等的注入訊息會略過。工具回傳是 `custom_tool_call_output`，不算新的一句。
- `token_usage_record.payload`：有唯一 `response_id`、單次 `usage`、累計 `turn_token_usage` 與 `thread_token_usage`。`usage` 的 `input_tokens` 已包含 `cached_input_tokens` 與 `cache_write_input_tokens`。
- `event_msg.payload.type = "token_count"`：又寫一次本次與累計用量，並有 `model_context_window`。

`CodexParser` 以 `response_id` 去重，只用 `token_usage_record.usage` 累加成本；`token_count` 只取 Codex 實際回報的可用視窗，不再算一次錢。舊記錄沒有 `token_usage_record` 時才會用 `last_token_usage` 當 fallback。解析器與 Claude 版一樣保存 byte offset，最後一行沒有換行時會留到下次再讀。

Codex hook 官方文件明說 `transcript_path` 只是便利欄位，其格式不是穩定介面。這是每次升級 CLI 都要用真實記錄再驗的主要風險。

## 快取、單價與上下文

2026-10-14 查到 GPT-5.6 以後的快取預設保證時間是最近寫入或命中後 30 分鐘，OpenAI 可能保留更久。因此面板 `TTL` 記 1800 秒，表示保證時間，不表示逾時必定失效。

`pricing.go` 的 OpenAI 價格是標準 API 文字牌價，包括這台機器實際使用的 `gpt-5.6-sol`。Codex 成本算法是：

```text
普通輸入 = input - cached - cache_write
成本 = 普通輸入×Input + cached×Cached + cache_write×Input×1.25 + output×Output
```

當單次輸入超過 272K，GPT-5.6／6 的整筆請求套長上下文價：輸入、快取讀寫 2 倍，輸出 1.5 倍。面板的 Window 優先用 rollout 實值；例如這台機器的實際回報是 258,400，不是直接顯示 API 型錄的 1,050,000。

## 安裝與移除

```powershell
aimon.exe install codex
aimon.exe uninstall codex
```

安裝會複製程式、合併 `~/.codex/hooks.json`、放入 Codex 導覽 skill。如果 hooks.json 已存在，先寫 `hooks.json.aimon-bak`；重複安裝會先移掉含 `ai-session-monitor` 標記的舊組再加一組，別人的 hook 保留。移除也只拿掉 aimon 的組。Claude 的安裝路徑與 JSON 處理沒有改變；裸 `install` 與 `uninstall` 仍是 Claude。

## 驗收

```powershell
$env:GOCACHE = "$PWD\.gocache"
$env:GOTMPDIR = "$PWD\.gotmp"
& 'C:\Program Files\Go\bin\go.exe' test ./...
& 'C:\Program Files\Go\bin\go.exe' vet ./...
& .\aimon.exe stats --agent codex <rollout.jsonl>
```

Codex parser 測試必須至少覆蓋：

1. 相同 `response_id` 與對應 `token_count` 不重複算用量。
2. `task_started` 會切斷一句，`custom_tool_call_output` 不會。
3. 只讀新增 bytes，沒有換行的半行不先吃掉。

實機驗收時，用實際 `codex exec` 產生新 session，在面板 API 查 `codex-<id>`；正常流程應為 `SessionStart=idle → UserPromptSubmit=working → Stop=idle`。若執行環境禁止 Codex 連網，只能驗到前兩段，不可宣稱已驗到 Stop。
