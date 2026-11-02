package httpapi

import (
	"fmt"
	"io/fs"
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

// handleDeleteLibraryVideo 把这部影片从本地库删掉：**磁盘上的文件一起删**，再删 library_files 记录。
//
// 为什么放在播放页上（而不是只留媒体库页那张卡）：用户是在「看」的时候发现「这部片占空间/
// 下错版本了」，让他回媒体库再找一遍是多余的一跳。
//
// 四个必须做的事：
//  1. 路径必须落在库根之内才删（和 /file、/art 同一道闸门，见 insideRoot）——记录被写脏、
//     或者库根被人改小之后，这条接口绝不能变成「按库里的字符串随便删本机文件」。
//  2. 先删文件、后删记录：文件删不掉（被播放器占着、权限不够）就**留着记录**，
//     否则媒体库里会剩一条指着空气的记录（卡片上那条「文件已不在磁盘上」就是这么来的）。
//  3. **整个文件夹端掉**，不是只删记录里那 5 个路径（2026-11-03 用户要求「目录和元数据都要删掉」）：
//     Emby 那种「一片一个文件夹」的结构，下载失败留下的半截文件、字幕、`thumb-sheet.jpg`
//     之类都不在记录里，只按记录删就剩一个装着垃圾的壳。端之前先问一句「这里面还有别人的
//     东西吗」（CountLibraryFilesUnder），有就退回逐个删，绝不碰别人的片。
//  4. 这条片派生出来的缓存一起清：合流成品（`data/mux/<id>-*.mp4`，4K 一档 500 MB 起步）
//     和下载工作目录（`data/tmp/dl-<id>-<lib>`）。不清的话盘上会留一堆再也没人读的大文件。
func (s *Server) handleDeleteLibraryVideo(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "影片 id 不对")
		return
	}
	files, err := s.st.ListLibraryFilesByVideo(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if len(files) == 0 {
		writeErr(w, http.StatusNotFound, "not_found", "本地没有这部电影")
		return
	}

	removed := make([]string, 0, len(files))
	kept := make([]map[string]any, 0, len(files))
	for _, lf := range files {
		lib, lerr := s.st.GetLibrary(lf.LibraryID)
		if lerr != nil {
			kept = append(kept, map[string]any{"libraryId": lf.LibraryID, "path": lf.VideoPath,
				"reason": "这个库已经不在了"})
			continue
		}
		root := filepath.Clean(lib.RootPath)
		videoPath := filepath.Clean(lf.VideoPath)
		if videoPath == "" || !insideRoot(root, videoPath) {
			// 越界的记录绝不删：宁可在界面上留着一条删不掉的，也不能按坏路径删本机文件
			s.log.Warn("库文件路径越界，拒绝删除", "video", lf.VideoID, "library", lf.LibraryID,
				"path", videoPath, "root", root)
			kept = append(kept, map[string]any{"libraryId": lf.LibraryID, "path": lf.VideoPath,
				"reason": "文件不在库目录内"})
			continue
		}
		if rmErr := os.Remove(videoPath); rmErr != nil && !os.IsNotExist(rmErr) {
			kept = append(kept, map[string]any{"libraryId": lf.LibraryID, "path": lf.VideoPath,
				"reason": "文件删不掉：" + rmErr.Error()})
			continue
		}
		removed = append(removed, videoPath)
		// 边角文件（nfo / 海报 / 剧照）跟着走：留着就是一堆指着不存在的片的孤儿图
		for _, p := range []string{lf.NFOPath, lf.PosterPath, lf.ThumbPath, lf.FanartPath, lf.StrmPath} {
			p = filepath.Clean(p)
			if p == "" || p == "." || !insideRoot(root, p) {
				continue
			}
			if _, statErr := os.Stat(p); statErr != nil {
				continue
			}
			if rmErr := os.Remove(p); rmErr == nil {
				removed = append(removed, p)
			}
		}
		// 剩下的散件（半截下载、字幕、临时图）连同文件夹一起端掉
		if extra := s.purgeVideoDir(root, videoPath); len(extra) > 0 {
			removed = append(removed, extra...)
		}
		pruneEmptyDirs(root, filepath.Dir(videoPath))
		s.clearVideoDerived(lf.VideoID, lf.LibraryID)
		if delErr := s.st.DeleteLibraryFile(lf.VideoID, lf.LibraryID); delErr != nil {
			s.log.Warn("删库记录失败", "video", lf.VideoID, "library", lf.LibraryID, "err", delErr)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      len(kept) == 0,
		"removed": removed,
		"kept":    kept,
	})
}

// purgeVideoDir 把这条片自己的那个文件夹整个删掉，返回删掉的文件清单。
//
// 三条硬性前提，任何一条不满足就退回「只删记录里那几个路径」：
//   - 文件夹在库根之内、且不是库根本身（有人的库是「根目录直接堆片」，那种情况清空文件夹
//     等于清空整个库）；
//   - 文件夹里没有别的 library_files 记录（`CountLibraryFilesUnder`）——rel_dir 被写脏成
//     两条片共用一个目录时，端文件夹就是替别人删片；
//   - 文件夹真是这条片的目录（`filepath.Dir(videoPath)`），不是别的什么路径。
func (s *Server) purgeVideoDir(root, videoPath string) []string {
	dir := filepath.Dir(filepath.Clean(videoPath))
	if dir == filepath.Clean(root) || !insideRoot(root, dir) {
		return nil
	}
	if n, err := s.st.CountLibraryFilesUnder(dir, filepath.Clean(videoPath)); err != nil || n > 0 {
		if err != nil {
			s.log.Warn("查同目录其它记录失败，退回逐个删", "dir", dir, "err", err)
		}
		return nil
	}
	// 先把要删的文件名列出来（删除之后就数不出来了），响应里要能看见究竟删了什么
	var entries []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			entries = append(entries, p)
		}
		return nil
	})
	if err := os.RemoveAll(dir); err != nil {
		s.log.Warn("删除影片目录失败", "dir", dir, "err", err)
		return nil
	}
	return entries
}

// clearVideoDerived 清掉这条片派生出来的大文件：合流的成品与下载工作目录。
//
// 合流缓存 `data/mux/<videoId>-<variant>.mp4` 是按「看的时候临时合一条 mp4」攒下来的，
// 实测 4K 一档 500 MB 起步（本机 mux 目录一度 512 MB / 13 个文件）；片子都删了它还留着，
// 就是纯粹的占空间。工作目录 `data/tmp/dl-<videoId>-<lib>` 是下载中途的暂存，失败的任务会留下它。
func (s *Server) clearVideoDerived(videoID, libraryID int64) {
	if s.cfg != nil && s.cfg.DataDir != "" {
		work := filepath.Join(s.cfg.DataDir, "tmp", fmt.Sprintf("dl-%d-%d", videoID, libraryID))
		if err := os.RemoveAll(work); err != nil {
			s.log.Warn("清理下载工作目录失败", "dir", work, "err", err)
		}
	}
	if s.mux != nil {
		prefix := fmt.Sprintf("%d-", videoID)
		for _, key := range s.mux.KeysWithPrefix(prefix) {
			if p := s.mux.Path(key); p != "" {
				if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
					s.log.Warn("清理合流缓存失败", "key", key, "err", err)
				}
			}
			s.mux.Clear(key)
		}
	}
}

// insideRoot 判断一个绝对路径是否落在库根之内（含根自身）。
// 「/api/videos/{id}/file」「…/art」和上面的删除接口共用这一份——按库根放行任意路径的地方，
// 只要有一处漏了校验，就是任意文件读 / 任意文件删。
func insideRoot(root, abs string) bool {
	root, abs = filepath.Clean(root), filepath.Clean(abs)
	return abs == root || strings.HasPrefix(abs, root+string(filepath.Separator))
}

// pruneEmptyDirs 从 dir 往上收空目录，收到 root（不含）为止。
// 只在「真空了」的时候删，删不动就停——绝不递归删有内容的目录。
func pruneEmptyDirs(root, dir string) {
	root = filepath.Clean(root)
	for dir = filepath.Clean(dir); dir != root && insideRoot(root, dir); dir = filepath.Dir(dir) {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
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
