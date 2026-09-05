package pipeline

// SetGate 实现 engine.ExecuteRound 的运行时注入接口（见 translate.go）。
// 各 LLM handler 的退避重试 select 通过 Gate.Done() 中止等待。
// 幂等：重复注入以最后一次为准；nil 恢复为无暂停语义。

func (h *TranslateHandler) SetGate(g *PauseGate) { h.Gate = g }

func (h *ExtractHandler) SetGate(g *PauseGate) { h.Gate = g }

func (h *AdjudicateHandler) SetGate(g *PauseGate) { h.Gate = g }

func (h *SemanticQAHandler) SetGate(g *PauseGate) { h.Gate = g }

func (h *ReviseHandler) SetGate(g *PauseGate) { h.Gate = g }

func (h *CorrectHandler) SetGate(g *PauseGate) {}
