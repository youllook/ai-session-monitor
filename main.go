// aimon：AI 對話監看。Claude Code 與 Codex 的 hook 呼叫它記錄每個對話的狀態，
// 面板用瀏覽器小視窗列出所有對話的長度、快取、花費與是否活躍；越過門檻時在對話裡提醒。
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

const version = "0.3.0"

const usage = `aimon %s：AI 對話監看

用法：
  aimon install [claude|codex]    安裝 hook＋導覽 skill（未指定時為 Claude）
  aimon uninstall [claude|codex]  移除對應的 hook 與導覽 skill
  aimon panel              打開清單面板
  aimon hook <claude|codex>   給工具的 hook 呼叫（從標準輸入讀事件）
  aimon stats [--agent codex] <記錄.jsonl>  驗證用：印出對話記錄的數字
  aimon version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Printf(usage, version)
		return
	}
	var err error
	switch os.Args[1] {
	case "hook":
		agent := "claude"
		if len(os.Args) > 2 {
			agent = os.Args[2]
		}
		runHook(agent, os.Stdin, os.Stdout)
	case "panel":
		runPanel()
	case "install":
		agent := "claude"
		if len(os.Args) > 2 {
			agent = os.Args[2]
		}
		if agent == "codex" {
			err = installCodex()
		} else if agent == "claude" {
			err = installClaude()
		} else {
			err = fmt.Errorf("安裝對象只接受 claude 或 codex")
		}
	case "uninstall":
		agent := "claude"
		if len(os.Args) > 2 {
			agent = os.Args[2]
		}
		if agent == "codex" {
			err = uninstallCodex()
		} else if agent == "claude" {
			err = uninstallClaude()
		} else {
			err = fmt.Errorf("移除對象只接受 claude 或 codex")
		}
	case "stats":
		// 驗證用：直接讀一份對話記錄，印出面板會顯示的數字
		if len(os.Args) < 3 {
			err = fmt.Errorf("用法：aimon stats [--agent codex] <對話記錄.jsonl>")
			break
		}
		agent, path := "claude", ""
		switch {
		case len(os.Args) == 3:
			path = os.Args[2]
		case len(os.Args) == 5 && os.Args[2] == "--agent":
			agent, path = os.Args[3], os.Args[4]
		default:
			err = fmt.Errorf("用法：aimon stats [--agent codex] <對話記錄.jsonl>")
			break
		}
		if err != nil {
			break
		}
		if agent != "claude" && agent != "codex" {
			err = fmt.Errorf("--agent 只接受 claude 或 codex")
			break
		}
		p := newParser(agent)
		if err = p.Update(path); err == nil {
			b, _ := json.MarshalIndent(p.Stats(), "", "  ")
			fmt.Println(string(b))
		}
	case "version":
		fmt.Println(version)
	default:
		fmt.Printf(usage, version)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "失敗：", err)
		os.Exit(1)
	}
}
