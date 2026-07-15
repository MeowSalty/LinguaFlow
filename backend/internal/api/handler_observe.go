package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	jobCount, err := s.entClient.Job.Query().Count(ctx)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	resourceCount, err := s.entClient.Resource.Query().Count(ctx)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	segmentCount, err := s.entClient.Segment.Query().Count(ctx)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	usageCount, err := s.entClient.UsageRecord.Query().Count(ctx)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	activityCount, err := s.entClient.ActivityLog.Query().Count(ctx)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = fmt.Fprintf(w, "# HELP linguaflow_jobs_total Total jobs persisted.\n")
	_, _ = fmt.Fprintf(w, "# TYPE linguaflow_jobs_total gauge\n")
	_, _ = fmt.Fprintf(w, "linguaflow_jobs_total %d\n", jobCount)
	_, _ = fmt.Fprintf(w, "# HELP linguaflow_resources_total Total resources persisted.\n")
	_, _ = fmt.Fprintf(w, "# TYPE linguaflow_resources_total gauge\n")
	_, _ = fmt.Fprintf(w, "linguaflow_resources_total %d\n", resourceCount)
	_, _ = fmt.Fprintf(w, "# HELP linguaflow_segments_total Total review segments persisted.\n")
	_, _ = fmt.Fprintf(w, "# TYPE linguaflow_segments_total gauge\n")
	_, _ = fmt.Fprintf(w, "linguaflow_segments_total %d\n", segmentCount)
	_, _ = fmt.Fprintf(w, "# HELP linguaflow_usage_records_total Total usage records persisted.\n")
	_, _ = fmt.Fprintf(w, "# TYPE linguaflow_usage_records_total gauge\n")
	_, _ = fmt.Fprintf(w, "linguaflow_usage_records_total %d\n", usageCount)
	_, _ = fmt.Fprintf(w, "# HELP linguaflow_activity_logs_total Total activity logs persisted.\n")
	_, _ = fmt.Fprintf(w, "# TYPE linguaflow_activity_logs_total gauge\n")
	_, _ = fmt.Fprintf(w, "linguaflow_activity_logs_total %d\n", activityCount)
}

func (s *Server) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	spec, err := GetSwagger()
	if err != nil {
		s.writeProblem(w, r, http.StatusInternalServerError, "openapi_error", "OpenAPI 规范加载失败")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(spec)
}

func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8" />
  <title>LinguaFlow API Docs</title>
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <style>
    body { margin: 0; font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    header { padding: 16px 24px; border-bottom: 1px solid #ddd; }
    main { padding: 24px; }
    code { background: #f6f8fa; padding: 2px 6px; border-radius: 4px; }
  </style>
</head>
<body>
  <header><h1>LinguaFlow Web Service API</h1></header>
  <main>
    <p>OpenAPI JSON: <a href="/api/openapi.json"><code>/api/openapi.json</code></a></p>
    <p>Swagger UI can load this endpoint as its specification source.</p>
  </main>
</body>
</html>`))
}
