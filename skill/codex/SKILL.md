---
name: ai-session-monitor
description: Codex 對話監看面板與花費提醒的導覽。當使用者問面板數字、花費提醒、快取、上下文、模型、思考強度或如何打開監看面板時使用。
---

# AI 對話監看：Codex 導覽

用白話回答使用者當下問的部分，不要整份照念。

## 面板數字

- 「對話」是最近一次模型請求的輸入 token／Codex 回報的可用上下文上限。
- `Cache` 第一格是最近一次命中或寫入快取的 token。GPT-5.6 以後官方保證至少 30 分鐘，可能保留更久。
- 模型名後面的 `low`／`medium`／`high`／`xhigh`／`max` 是這回合的思考強度。
- 金額是依 API 牌價估算；ChatGPT 訂閱不會依這個金額扣款。第一個金額是這一句觸發的所有請求，`Total` 是全對話累計。
- 綠色閃燈是執行中，黃色是閒置且快取仍在保證時間內，灰色是已超過該時間或對話結束。

## 怎麼控制對話

- 換一件事時用 `/clear` 或 `/new` 開新對話。
- 仍需要前文但對話太長時用 `/compact`。
- 用 `/status` 看 Codex 自己回報的上下文與用量，用 `/model` 選模型與思考強度。
- 要檢視、信任或停用監看 hook，用 `/hooks`。

## 打開面板

Windows 在終端執行 `%AppData%\ai-session-monitor\bin\aimon.exe panel`。開新 Codex 對話時，面板沒在跑也會自動打開。
