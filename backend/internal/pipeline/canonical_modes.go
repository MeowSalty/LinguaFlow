package pipeline

// The six built-in mode identities use fixed bits, never one private set per
// resource/round. Translate retains its existing status-based selection.
func CompletedModeMask(mode string) uint8 {
	switch mode {
	case RoundModeExtract:
		return 1
	case RoundModeSemanticQA:
		return 2
	case RoundModeAdjudicate:
		return 4
	case RoundModeRevise:
		return 8
	case "correct":
		return 16
	}
	return 0
}
