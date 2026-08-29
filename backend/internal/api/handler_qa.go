package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// handleQaRecheck 按执行策略对既有译文重跑 QA 重检，
// 返回新增/清除问题数与逐资源统计摘要，不修改译文与段落状态。
func (s *Server) handleQaRecheck(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := s.parseIntParam(w, r, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}

	var req QaRecheckRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if req.ProfileId <= 0 {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "profile_id 必须是正整数")
		return
	}

	// 生成类型的可选数组字段为指针，解引用后传给 service（nil 保持为 nil）。
	var resourceIDs []int
	if req.ResourceIds != nil {
		resourceIDs = *req.ResourceIds
	}
	var segmentIDs []int
	if req.SegmentIds != nil {
		segmentIDs = *req.SegmentIds
	}
	var segmentGroupKeys []string
	if req.SegmentGroupKeys != nil {
		segmentGroupKeys = *req.SegmentGroupKeys
	}

	res, err := s.qaRecheck.Recheck(r.Context(), authUser.User.ID, projectID, service.QARecheckInput{
		ProfileID:        req.ProfileId,
		ResourceIDs:      resourceIDs,
		SegmentIDs:       segmentIDs,
		SegmentGroupKeys: segmentGroupKeys,
	})
	if err != nil {
		s.writeQaRecheckServiceError(w, r, err)
		return
	}

	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{
		ActorUserID:  authUser.User.ID,
		ProjectID:    &projectID,
		Action:       "qa.recheck",
		ResourceType: "project",
		ResourceID:   projectID,
		Message:      "执行 QA 重检",
		Metadata: map[string]any{
			"profile_id":        res.ProfileID,
			"resources_checked": res.ResourcesChecked,
			"segments_checked":  res.SegmentsChecked,
			"issues_new":        res.IssuesNew,
		},
	})
	writeJSON(w, http.StatusOK, toQaRecheckResult(*res))
}

// writeQaRecheckServiceError 将 QA 重检 service 错误映射为 Problem 响应。
func (s *Server) writeQaRecheckServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrProjectNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "项目不存在")
	case errors.Is(err, service.ErrExecutionProfileNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "执行策略不存在")
	case errors.Is(err, service.ErrResourceNotFound), errors.Is(err, service.ErrSegmentNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, service.ErrForbidden):
		s.writeProblem(w, r, http.StatusForbidden, "forbidden", "没有权限执行该操作")
	case errors.Is(err, service.ErrQAProfileDisabled):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "该执行策略未启用 QA 检查")
	case errors.Is(err, service.ErrRecheckTooLarge):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "重检范围过大，请缩小范围后重试")
	case errors.Is(err, service.ErrInvalidInput):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", err.Error())
	default:
		s.writeProjectServiceError(w, r, err)
	}
}

// toQaRecheckResult 将 service 层重检结果映射为生成的 QaRecheckResult 响应类型。
func toQaRecheckResult(res service.QARecheckResult) QaRecheckResult {
	resources := make([]QaRecheckResourceResult, 0, len(res.Resources))
	for _, item := range res.Resources {
		resources = append(resources, QaRecheckResourceResult{
			DispositionsInherited:     item.DispositionsInherited,
			IssuesCleared:             item.IssuesCleared,
			IssuesNew:                 item.IssuesNew,
			ResourceId:                item.ResourceID,
			ResourceName:              item.ResourceName,
			SegmentsChecked:           item.SegmentsChecked,
			SegmentsSkippedConcurrent: item.SegmentsSkippedConcurrent,
			SegmentsSkippedNoTarget:   item.SegmentsSkippedNoTarget,
		})
	}
	busy := make([]QaRecheckBusyResource, 0, len(res.ResourcesSkippedBusy))
	for _, item := range res.ResourcesSkippedBusy {
		busy = append(busy, QaRecheckBusyResource{
			ActiveJobId: item.ActiveJobID,
			ResourceId:  item.ResourceID,
		})
	}
	return QaRecheckResult{
		DispositionsInherited:     res.DispositionsInherited,
		IssuesCleared:             res.IssuesCleared,
		IssuesNew:                 res.IssuesNew,
		ProfileId:                 res.ProfileID,
		ProfileName:               res.ProfileName,
		Resources:                 resources,
		ResourcesChecked:          res.ResourcesChecked,
		ResourcesSkippedBusy:      busy,
		SegmentsChecked:           res.SegmentsChecked,
		SegmentsSkippedConcurrent: res.SegmentsSkippedConcurrent,
		SegmentsSkippedNoTarget:   res.SegmentsSkippedNoTarget,
	}
}
