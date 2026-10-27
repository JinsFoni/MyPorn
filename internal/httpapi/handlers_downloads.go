package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/mario/myporn/internal/jobs"
	"github.com/mario/myporn/internal/library"
	"github.com/mario/myporn/internal/model"
)

// handleDownloadOptions 返回下载确认框需要的信息：可选库、空间、预估体积。
func (s *Server) handleDownloadOptions(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	v, err := s.st.GetVideo(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "影片不存在")
		return
	}

	libs, err := s.st.ListLibraries()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	usage, _ := s.st.LibraryUsages()
	libDTOs := make([]map[string]any, 0, len(libs))
	defaultID := int64(0)
	for _, l := range libs {
		if !l.Enabled {
			continue
		}
		libDTOs = append(libDTOs, s.libraryDTO(l, usage))
		if l.IsDefault && defaultID == 0 {
			defaultID = l.ID
		}
	}
	if defaultID == 0 && len(libDTOs) > 0 {
		if first, ok := libDTOs[0]["id"].(int64); ok {
			defaultID = first
		}
	}

	// 预估体积：需要最高画质的码率，解析失败不影响下载（任务里会重试）。
	var (
		variantID     string
		bestHeight    int
		bestBandwidth int
		estimated     int64
		estimateKnown bool
	)
	if variants, _, verr := s.ensureVariants(r, v, true); verr == nil && len(variants) > 0 {
		best := variants[0]
		variantID = best.ID()
		bestHeight = best.Height
		bestBandwidth = best.Bandwidth
		if est := best.EstimateBytes(v.DurationSec); est > 0 {
			estimated = est
			estimateKnown = true
		}
	}

	local, _ := s.st.ListLibraryFilesByVideo(id)
	localDTO := make([]map[string]any, 0, len(local))
	for _, f := range local {
		localDTO = append(localDTO, map[string]any{
			"libraryId":  f.LibraryID,
			"path":       f.VideoPath,
			"sizeBytes":  f.SizeBytes,
			"resolution": resLabel(f.Height),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"videoId":          id,
		"height":           bestHeight,
		"bandwidth":        bestBandwidth,
		"libraries":        libDTOs,
		"defaultLibraryId": nullableInt64(defaultID),
		"variantId":        nullableString(variantID),
		"estimatedBytes":   nullableInt64When(estimated, estimateKnown),
		"durationSec":      v.DurationSec,
		"localCopies":      localDTO,
	})
}

func resLabel(height int) string {
	v := model.Variant{Height: height}
	if height == 0 {
		return ""
	}
	return v.ID()
}

func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullableInt64When(v int64, ok bool) any {
	if !ok || v <= 0 {
		return nil
	}
	return v
}

// handleCreateDownload 投递下载任务（202）。
func (s *Server) handleCreateDownload(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	v, err := s.st.GetVideo(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "影片不存在")
		return
	}

	var in struct {
		LibraryID *int64 `json:"libraryId"`
		Overwrite bool   `json:"overwrite"`
	}
	if r.ContentLength > 0 {
		body := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
		if err := body.Decode(&in); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", "请求体非法")
			return
		}
	}

	libID := int64(0)
	if in.LibraryID != nil && *in.LibraryID > 0 {
		libID = *in.LibraryID
	} else if def, derr := s.st.DefaultLibrary(); derr == nil {
		libID = def.ID
	}
	if libID == 0 {
		writeErr(w, http.StatusBadRequest, "no_library", "还没有可用的本地库，请先在设置里创建")
		return
	}
	lib, err := s.st.GetLibrary(libID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "指定的本地库不存在")
		return
	}

	// 幂等：已有本地副本且未要求覆盖 → 直接返回 skipped。
	if existing, eerr := s.st.GetLibraryFile(id, libID); eerr == nil && !in.Overwrite {
		writeJSON(w, http.StatusOK, map[string]any{
			"skipped":   true,
			"jobId":     nil,
			"libraryId": libID,
			"reason":    "该影片在此库中已存在",
			"localPath": existing.VideoPath,
			"sizeBytes": existing.SizeBytes,
		})
		return
	}
	if active, err := s.st.HasActiveJobForTarget(model.JobTypeDownload, id); err == nil && active {
		writeErr(w, http.StatusConflict, "already_queued", "这部影片已经在下载队列里了")
		return
	}
	if err := library.EnsureWritable(lib.RootPath); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "库目录不可写: "+err.Error())
		return
	}

	// YouTube 这类"没有平台 adapter"的影片只有 yt-dlp 一条路：
	// 从 videos.platform_video_id 反查 yt_videos，交给 yt_download，
	// 否则详情页那个「下载到本地库」按钮点了就是一个 unsupported_platform 错误。
	if _, ok := s.reg.Get(v.Platform); !ok {
		ytRow, yerr := s.st.GetYtVideoByVideoID(v.PlatformVideoID)
		if yerr != nil {
			writeErr(w, http.StatusBadRequest, "unsupported_platform", "这个平台没有在线播放通道，只能从 YouTube 页下载")
			return
		}
		jobID, jerr := s.runner.EnqueueYtDownload(ytRow.ID, libID, in.Overwrite)
		if jerr != nil {
			writeErr(w, http.StatusInternalServerError, "internal", jerr.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"skipped":   false,
			"jobId":     jobID,
			"libraryId": libID,
			"videoId":   v.ID,
			"title":     v.Title,
		})
		return
	}

	jobID, err := s.runner.EnqueueDownload(id, libID, in.Overwrite)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"skipped":   false,
		"jobId":     jobID,
		"libraryId": libID,
		"videoId":   v.ID,
		"title":     v.Title,
	})
}

// handleListDownloads 返回下载队列 + 历史（含影片标题与所属库名）。
func (s *Server) handleListDownloads(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	filter := r.URL.Query().Get("status")
	// YouTube 下载也进这个队列：任务类型不同，用户在「下载」页看到的却应该是同一件事。
	items, err := s.st.ListJobsByTypes([]string{model.JobTypeDownload, model.JobTypeYtDownload}, filter, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	libs, _ := s.st.ListLibraries()
	libNames := make(map[int64]string, len(libs))
	for _, l := range libs {
		libNames[l.ID] = l.Name
	}

	out := make([]map[string]any, 0, len(items))
	for _, j := range items {
		dto := map[string]any{
			"id":         j.ID,
			"type":       j.Type,
			"status":     j.Status,
			"progress":   j.Progress,
			"message":    j.Message,
			"error":      j.Error,
			"targetId":   j.TargetID,
			"bytesDone":  j.BytesDone,
			"bytesTotal": j.BytesTotal,
			"speedBps":   j.SpeedBPS,
			"createdAt":  rfc3339(&j.CreatedAt),
			"startedAt":  rfc3339(j.StartedAt),
			"finishedAt": rfc3339(j.FinishedAt),
		}
		var p jobs.DownloadPayloadInfo
		if jobs.ParseDownloadPayload(j.Payload, &p) == nil {
			dto["libraryId"] = p.LibraryID
			dto["libraryName"] = libNames[p.LibraryID]
			dto["videoId"] = p.VideoID
		}
		if v, err := s.st.GetVideo(j.TargetID); err == nil {
			dto["title"] = v.Title
			dto["thumbUrl"] = imgProxy(v.Platform, v.ThumbURL)
			dto["durationSec"] = v.DurationSec
			dto["platform"] = v.Platform
		}
		if lf, err := s.st.GetLibraryFile(j.TargetID, p.LibraryID); err == nil {
			dto["localPath"] = lf.VideoPath
			dto["sizeBytes"] = lf.SizeBytes
		}
		out = append(out, dto)
	}

	active, _ := s.st.ActiveJobCount(model.JobTypeDownload)
	if n, err := s.st.ActiveJobCount(model.JobTypeYtDownload); err == nil {
		active += n
	}
	usage, _ := s.st.LibraryUsages()
	var totalSize int64
	for _, u := range usage {
		totalSize += u.SizeBytes
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":          out,
		"activeCount":    active,
		"totalSizeBytes": totalSize,
	})
}

// handlePauseJob / handleResumeJob / handleRetryJob 组成任务控制三件套（FR-23）。
func (s *Server) handlePauseJob(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	status, err := s.st.RequestPause(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "任务不存在")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": status})
}

func (s *Server) handleResumeJob(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	ok, err := s.st.RetryJob(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_request", "只有失败/取消/暂停的任务可以恢复")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	s.handleResumeJob(w, r)
}

// handleDeleteJob 从历史里移除一条下载记录（只删任务记录，不动文件）。
func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	j, err := s.st.GetJob(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "任务不存在")
		return
	}
	if j.Status == model.JobRunning || j.Status == model.JobQueued {
		writeErr(w, http.StatusBadRequest, "bad_request", "任务还在队列里，请先取消")
		return
	}
	if err := s.st.DeleteJob(id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func trimLower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
