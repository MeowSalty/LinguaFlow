package execution

import "errors"

// ValidateBatchLimits 校验已保存的分批上限，不会替换显式零值。
// extract 允许两个上限同时为 0，表示一次发送全部选中的段落。
// 本地纠错（correct）没有分批概念，绝不能调用本校验器。
func ValidateBatchLimits(mode string, batchSize, maxWordsPerBatch int) error {
	switch mode {
	case "translate", "extract", "adjudicate", "semantic_qa", "revise", "ruby_retry":
	default:
		return errors.New("batch limits require an LLM round mode")
	}
	if batchSize < 0 {
		return errors.New("batch_size must be >= 0")
	}
	if maxWordsPerBatch < 0 {
		return errors.New("max_words_per_batch must be >= 0")
	}
	if mode != "extract" && batchSize == 0 && maxWordsPerBatch == 0 {
		return errors.New("batch_size and max_words_per_batch cannot both be 0")
	}
	return nil
}
