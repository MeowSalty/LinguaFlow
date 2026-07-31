package backend

import (
	"context"
	"sync/atomic"
)

type MeteredBackend struct {
	inner        Backend
	apiCalls     atomic.Int64
	inputTokens  atomic.Int64
	outputTokens atomic.Int64
}

func NewMeteredBackend(inner Backend) *MeteredBackend {
	return &MeteredBackend{inner: inner}
}

func (m *MeteredBackend) Name() string { return m.inner.Name() }

func (m *MeteredBackend) Translate(ctx context.Context, req Request) (*Response, error) {
	m.apiCalls.Add(1)
	resp, err := m.inner.Translate(ctx, req)
	if err != nil {
		return resp, err
	}
	if resp != nil {
		m.inputTokens.Add(resp.Usage.PromptTokens)
		m.outputTokens.Add(resp.Usage.CompletionTokens)
	}
	return resp, nil
}

func (m *MeteredBackend) Close() error { return m.inner.Close() }

func (m *MeteredBackend) Metrics() MeterMetrics {
	return MeterMetrics{
		APICalls:     m.apiCalls.Load(),
		InputTokens:  m.inputTokens.Load(),
		OutputTokens: m.outputTokens.Load(),
	}
}

type MeterMetrics struct {
	APICalls     int64
	InputTokens  int64
	OutputTokens int64
}
