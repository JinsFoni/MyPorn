package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/mario/myporn/internal/library"
	"github.com/mario/myporn/internal/model"
	"github.com/mario/myporn/internal/store"
)

// libraryDTO 附带磁盘空间与索引统计（FR-21 / FR-30）。
func (s *Server) libraryDTO(l *model.Library, usage map[int64]store.LibraryUsage) map[string]any {
	dto := map[string]any{
		"id":             l.ID,
		"name":           l.Name,
		"rootPath":       l.RootPath,
		"enabled":        l.Enabled,
		"isDefault":      l.IsDefault,
		"uploadPolicy":   l.UploadPolicy,
		"openlistPath":   l.OpenListPath,
		"namingTemplate": l.NamingTemplate,
	}
	sp := library.CheckSpace(l.RootPath)
	dto["exists"] = sp.Exists
	dto["writable"] = sp.Writable
	dto["freeBytes"] = sp.FreeBytes
	dto["totalBytes"] = sp.TotalBytes
	dto["spaceMessage"] = sp.Message
	if u, ok := usage[l.ID]; ok {
		dto["fileCount"] = u.FileCount
		dto["sizeBytes"] = u.SizeBytes
	} else {
		dto["fileCount"] = 0
		dto["sizeBytes"] = 0
	}
	return dto
}

func (s *Server) handleListLibraries(w http.ResponseWriter, r *http.Request) {
	libs, err := s.st.ListLibraries()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	usage, err := s.st.LibraryUsages()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	items := make([]map[string]any, 0, len(libs))
	var totalFiles, totalSize int64
	hasDefault := false
	for _, l := range libs {
		if l.IsDefault {
			hasDefault = true
			break
		}
	}
	for i, l := range libs {
		// 老数据可能一个默认库都没标（早期版本建的），这里按“第一个可用库”兜底，
		// 与 DefaultLibrary() 的取法保持一致，免得界面上没有「默认」标记。
		if !hasDefault && i == 0 && l.Enabled {
			l.IsDefault = true
		}
		items = append(items, s.libraryDTO(l, usage))
	}
	for _, u := range usage {
		totalFiles += u.FileCount
		totalSize += u.SizeBytes
	}
	// 首次使用时自动建议一个默认库（./media），前端据此引导创建。
	defaultRoot := s.cfg.LibraryRoot
	if v, err := s.st.GetSetting("library_root"); err == nil && strings.TrimSpace(v) != "" {
		defaultRoot = v
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":             items,
		"totalFileCount":    totalFiles,
		"totalSizeBytes":    totalSize,
		"suggestedRootPath": defaultRoot,
	})
}

type libraryInput struct {
	Name           *string `json:"name"`
	RootPath       *string `json:"rootPath"`
	Enabled        *bool   `json:"enabled"`
	IsDefault      *bool   `json:"isDefault"`
	UploadPolicy   *string `json:"uploadPolicy"`
	NamingTemplate *string `json:"namingTemplate"`
	OpenListPath   *string `json:"openlistPath"`
}

const (
	maxRootPathLen = 512
	defaultLibName = "默认库"
)

func validPolicy(p string) bool {
	switch p {
	case model.PolicyLocal, model.PolicyLocalRemote, model.PolicyStrm:
		return true
	}
	return false
}

func (s *Server) handleCreateLibrary(w http.ResponseWriter, r *http.Request) {
	var in libraryInput
	body := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := body.Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体非法")
		return
	}
	root := ""
	if in.RootPath != nil {
		root = strings.TrimSpace(*in.RootPath)
	}
	if root == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "库目录不能为空")
		return
	}
	if len(root) > maxRootPathLen {
		writeErr(w, http.StatusBadRequest, "bad_request", "路径过长")
		return
	}
	// 必须可写，否则不保存（FR-21）。
	sp := library.CheckSpace(root)
	if !sp.Writable {
		writeErr(w, http.StatusBadRequest, "bad_request", "目录不可写或无法创建: "+sp.Message)
		return
	}

	lib := &model.Library{
		Name:           defaultLibName,
		RootPath:       root,
		Enabled:        true,
		UploadPolicy:   model.PolicyLocal,
		NamingTemplate: "",
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
		lib.Name = strings.TrimSpace(*in.Name)
	}
	if in.Enabled != nil {
		lib.Enabled = *in.Enabled
	}
	if in.IsDefault != nil {
		lib.IsDefault = *in.IsDefault
	} else {
		// 第一个库自动成为默认库。
		if libs, err := s.st.ListLibraries(); err == nil && len(libs) == 0 {
			lib.IsDefault = true
		}
	}
	if in.UploadPolicy != nil && validPolicy(*in.UploadPolicy) {
		lib.UploadPolicy = *in.UploadPolicy
	}
	if in.NamingTemplate != nil {
		lib.NamingTemplate = strings.TrimSpace(*in.NamingTemplate)
	}
	if in.OpenListPath != nil {
		lib.OpenListPath = strings.TrimSpace(*in.OpenListPath)
	}

	id, err := s.st.CreateLibrary(lib)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			writeErr(w, http.StatusBadRequest, "bad_request", "这个目录已经是一个库了")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	lib.ID = id
	usage, _ := s.st.LibraryUsages()
	writeJSON(w, http.StatusCreated, s.libraryDTO(lib, usage))
}

func (s *Server) handlePatchLibrary(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	lib, err := s.st.GetLibrary(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "本地库不存在")
		return
	}
	var in libraryInput
	body := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := body.Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体非法")
		return
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
		lib.Name = strings.TrimSpace(*in.Name)
	}
	if in.RootPath != nil {
		root := strings.TrimSpace(*in.RootPath)
		if root == "" || len(root) > maxRootPathLen {
			writeErr(w, http.StatusBadRequest, "bad_request", "库目录非法")
			return
		}
		if sp := library.CheckSpace(root); !sp.Writable {
			writeErr(w, http.StatusBadRequest, "bad_request", "目录不可写或无法创建: "+sp.Message)
			return
		}
		lib.RootPath = root
	}
	if in.Enabled != nil {
		lib.Enabled = *in.Enabled
	}
	if in.IsDefault != nil {
		lib.IsDefault = *in.IsDefault
	}
	if in.UploadPolicy != nil && validPolicy(*in.UploadPolicy) {
		lib.UploadPolicy = *in.UploadPolicy
	}
	if in.NamingTemplate != nil {
		lib.NamingTemplate = strings.TrimSpace(*in.NamingTemplate)
	}
	if in.OpenListPath != nil {
		lib.OpenListPath = strings.TrimSpace(*in.OpenListPath)
	}
	if err := s.st.UpdateLibrary(lib); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	usage, _ := s.st.LibraryUsages()
	writeJSON(w, http.StatusOK, s.libraryDTO(lib, usage))
}

func (s *Server) handleDeleteLibrary(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if _, err := s.st.GetLibrary(id); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "本地库不存在")
		return
	}
	// 只删索引，不动磁盘文件（避免误删用户媒体）。
	if err := s.st.DeleteLibrary(id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleValidateLibraryPath 供新建库时即时校验路径（不落库）。
func (s *Server) handleValidateLibraryPath(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RootPath string `json:"rootPath"`
	}
	body := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	if err := body.Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体非法")
		return
	}
	sp := library.CheckSpace(strings.TrimSpace(in.RootPath))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         sp.Writable,
		"exists":     sp.Exists,
		"writable":   sp.Writable,
		"freeBytes":  sp.FreeBytes,
		"totalBytes": sp.TotalBytes,
		"message":    sp.Message,
	})
}
