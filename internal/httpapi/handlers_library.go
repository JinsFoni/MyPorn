package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/store"
)

// 媒体库：**磁盘上已经有的**影片（library_files），一页摊开。
//
// 和「下载」页（/api/downloads）不是一回事：那边是任务队列——还在跑的、失败的、排队的；
// 这边只认落盘记录，所以它同时也是「本地库到底有什么」的唯一入口（不用开文件管理器翻目录）。
// 播放走已有的 /api/videos/{id}/file（Range 续传），卡片封面优先用下载时落盘的那张 thumb.jpg，
// 远端封面地址过期也不影响这一页。

func (s *Server) handleListLibraryVideos(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.LibraryVideoQuery{
		LibraryID: int64(queryInt(r, "libraryId", 0)),
		Platform:  strings.TrimSpace(q.Get("platform")),
		Query:     strings.TrimSpace(q.Get("q")),
		Sort:      strings.TrimSpace(q.Get("sort")),
		Cursor:    q.Get("cursor"),
		Limit:     queryInt(r, "limit", 24),
	}
	items, next, err := s.st.ListLibraryVideos(filter)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	total, totalBytes, counters, err := s.st.LibraryVideoSummary(filter)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, libraryVideoDTO(it))
	}
	byLib := make([]map[string]any, 0, len(counters))
	for _, c := range counters {
		byLib = append(byLib, map[string]any{
			"libraryId": c.LibraryID,
			"name":      c.Name,
			"fileCount": c.FileCount,
			"sizeBytes": c.SizeBytes,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":          out,
		"nextCursor":     nullableString(next),
		"total":          total,
		"totalSizeBytes": totalBytes,
		"byLibrary":      byLib,
	})
}

// libraryVideoDTO 把一行落盘记录 + 影片信息拼成卡片要的东西。
func libraryVideoDTO(it *store.LibraryVideo) map[string]any {
	f, v := it.File, it.Video
	title := strings.TrimSpace(v.Title)
	if title == "" {
		title = strings.TrimSuffix(f.VideoName, filepath.Ext(f.VideoName))
	}
	// 落盘时 probe 出来的时长比 videos 表里的更贴身（片头片尾被剪过的情况），优先它
	duration := f.DurationSec
	if duration <= 0 {
		duration = v.DurationSec
	}
	names := make([]string, 0, len(v.Performers))
	for _, p := range v.Performers {
		names = append(names, p.Name)
	}

	// 卡片左上那颗圆头像 + 副行左边那个名字：平台影片取第一位艺人（B 站就是 UP 主），
	// YouTube 影片没有 performers，取频道（yt_channels）。
	// 都没有就留空——前端退化成首字母，不会画一个破图。
	// 代理按**头像所属的平台**走（艺人自己的 platform），不是影片的：图床认 Referer，
	// 拿影片的 platform 去拉一张跨站的头像会被图床挡掉。
	whoName, whoAvatar, whoPlatform := "", "", v.Platform
	if len(v.Performers) > 0 {
		whoName = v.Performers[0].Name
		whoAvatar = v.Performers[0].AvatarURL
		if v.Performers[0].Platform != "" {
			whoPlatform = v.Performers[0].Platform
		}
	}
	if whoAvatar == "" && it.ChannelAvatar != "" {
		whoAvatar = it.ChannelAvatar
		if whoName == "" {
			whoName = it.ChannelName
		}
	}

	// 本地封面只在这张图真的还在磁盘上时才给地址：前端拿到 404 只能退成占位图标，
	// 而远端那张（videos.thumb_url）通常也还在，白丢一次兜底机会。
	localThumb := ""
	if p := strings.TrimSpace(f.ThumbPath); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			localThumb = "/api/videos/" + strconv.FormatInt(f.VideoID, 10) + "/art?libraryId=" +
				strconv.FormatInt(f.LibraryID, 10) + "&k=thumb"
		}
	}
	// 文件被挪走/删掉时要让用户看见，别画一张点进去播不动的卡
	missing := false
	if st, err := os.Stat(filepath.Clean(f.VideoPath)); err != nil || st.IsDir() {
		missing = true
	}

	created := f.CreatedAt
	var createdPtr *time.Time
	if !created.IsZero() {
		createdPtr = &created
	}
	return map[string]any{
		"videoId":       f.VideoID,
		"libraryId":     f.LibraryID,
		"libraryName":   it.LibraryName,
		"title":         title,
		"fileName":      f.VideoName,
		"platform":      v.Platform,
		"performers":    names,
		"channelName":   whoName,
		"avatarUrl":     nullableString(imgProxy(whoPlatform, whoAvatar)),
		"durationSec":   duration,
		"width":         f.Width,
		"height":        f.Height,
		"sizeBytes":     f.SizeBytes,
		"downloadedAt":  rfc3339(createdPtr),
		"thumbUrl":      imgProxy(v.Platform, v.ThumbURL),
		"localThumbUrl": nullableString(localThumb),
		"videoPath":     f.VideoPath,
		"relDir":        f.RelDir,
		"missing":       missing,
		"fileUrl":       localFileURL(f.VideoID, f.LibraryID),
	}
}

// handleVideoArt 发本地库里那张海报/剧照（下载时一起落盘的 thumb.jpg / poster.jpg / fanart.jpg）。
//
// 为什么不复用 /api/img：那是远端代理，地址一过期就空白；这页要的是**本地文件**。
// 路径一律按「必须在库根之内」校验——和 /api/videos/{id}/file 同一道闸门（见 serveLibraryPath）。
func (s *Server) handleVideoArt(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "影片 id 不对")
		return
	}
	libID := int64(queryInt(r, "libraryId", 0))

	var lf *model.LibraryFile
	if libID > 0 {
		lf, err = s.st.GetLibraryFile(id, libID)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "这个库里没有这部电影")
			return
		}
	} else if files, ferr := s.st.ListLibraryFilesByVideo(id); ferr == nil && len(files) > 0 {
		lf = files[0]
	} else {
		writeErr(w, http.StatusNotFound, "not_found", "本地没有这部电影")
		return
	}

	var path string
	switch strings.TrimSpace(r.URL.Query().Get("k")) {
	case "poster":
		path = lf.PosterPath
	case "fanart":
		path = lf.FanartPath
	default:
		path = lf.ThumbPath
	}
	if strings.TrimSpace(path) == "" {
		writeErr(w, http.StatusNotFound, "not_found", "这张图没有落盘")
		return
	}
	// 图片是给 <img> 直接引的，缓存一天：换封面会换 video/library 的路径，不会串图。
	w.Header().Set("Cache-Control", "private, max-age=86400")
	s.serveLibraryPath(w, r, lf.LibraryID, path)
}
