package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mario/myporn/internal/imgcache"
	"github.com/mario/myporn/internal/library"
	"github.com/mario/myporn/internal/model"
	"github.com/mario/myporn/internal/social"
	"github.com/mario/myporn/internal/store"
)

// 社交动态任务（M5）：
//   - social_sync：抓一个（或全部）博主的新帖子，落库成照片墙；
//   - social_save：把选中的照片存进本地库。
//
// 两条任务都只有一个并发单位（sync 固定 1），因为两个站的限流都是按 IP 的，
// 并发抓只会更快撞墙，见 docs/M5-SOCIAL.md §5.4。

type socialSyncPayload struct {
	AccountID int64  `json:"accountId"` // 0 = 所有启用的博主
	Mode      string `json:"mode"`      // auto | full
}

// EnqueueSocialSync 投递一次博主同步；同一博主已有排队/在跑的任务就不重复投。
func (r *Runner) EnqueueSocialSync(accountID int64, mode string) (int64, error) {
	if mode == "" {
		mode = "auto"
	}
	if active, err := r.st.HasActiveJobForTarget(model.JobTypeSocialSync, accountID); err == nil && active {
		return 0, nil
	}
	return r.st.EnqueueTarget(model.JobTypeSocialSync, accountID, socialSyncPayload{AccountID: accountID, Mode: mode}, 8)
}

// socialPagesPerRun 是一次回填最多翻几页：
// Instagram 一页 12 帖，5 页 60 帖；X 没有翻页，一页就到底。
// 不设上限的话，刚添加博主时会连着几十次请求，直接吃 429。
const socialPagesPerRun = 5

// maxSyncErrorLen 是写进 social_accounts.last_error 的最大长度。
const maxSyncErrorLen = 300

// runSocialSyncJob 是 social_sync 的执行入口。
func (r *Runner) runSocialSyncJob(ctx context.Context, j *model.Job, c *Ctl) error {
	var p socialSyncPayload
	if j.Payload != "" {
		if err := json.Unmarshal([]byte(j.Payload), &p); err != nil {
			return fmt.Errorf("任务参数损坏")
		}
	}
	if r.social == nil {
		return fmt.Errorf("社交适配器未注册")
	}

	// 顺手把「艺人主页上挂的社交链接」和已有博主对一遍，纯本地操作、不花钱。
	if _, err := r.st.LinkSocialAccountsToPerformers(); err != nil {
		r.log.Warn("关联艺人与社交账号失败", "err", err)
	}

	var accounts []*store.SocialAccount
	if p.AccountID > 0 {
		acc, err := r.st.GetSocialAccount(p.AccountID)
		if err != nil {
			return fmt.Errorf("博主不存在: %w", err)
		}
		accounts = []*store.SocialAccount{acc}
	} else {
		list, err := r.st.ListSocialAccounts(true)
		if err != nil {
			return err
		}
		accounts = list
	}
	if len(accounts) == 0 {
		c.Progress(100, "没有需要同步的博主", 0, 0, 0)
		return nil
	}

	failed := 0
	for i, acc := range accounts {
		if err := c.Check(); err != nil {
			return err
		}
		label := fmt.Sprintf("%s/%d", social.PlatformNames()[acc.Platform], i+1)
		c.Progress(float64(i)/float64(len(accounts))*100,
			fmt.Sprintf("同步 %s @%s（%s）", platformName(acc.Platform), acc.Handle, label), 0, 0, 0)

		err := r.syncSocialAccount(ctx, c, acc, p.Mode)
		if err != nil {
			failed++
			r.recordSocialError(acc.ID, err)
			// 限流和未登录会持续影响后面的博主，直接收工，别把配额浪费在必然失败的请求上。
			if k := social.KindOf(err); k == social.KindRateLimited || k == social.KindNeedsAuth {
				c.Progress(100, fmt.Sprintf("@%s 失败：%v", acc.Handle, err), 0, 0, 0)
				return nil
			}
			continue
		}
		r.recordSocialError(acc.ID, nil)
	}

	if failed > 0 {
		c.Progress(100, fmt.Sprintf("同步完成，%d 个博主失败", failed), 0, 0, 0)
		return nil
	}
	c.Progress(100, fmt.Sprintf("同步完成，共 %d 个博主", len(accounts)), 0, 0, 0)
	return nil
}

func platformName(key string) string {
	if n, ok := social.PlatformNames()[key]; ok {
		return n
	}
	return key
}

// recordSocialError 把失败原因写回博主行，界面上能直接看到；err=nil 表示清空。
func (r *Runner) recordSocialError(accountID int64, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
		if len(msg) > maxSyncErrorLen {
			msg = msg[:maxSyncErrorLen]
		}
	}
	if err := r.st.SetSocialAccountError(accountID, msg); err != nil {
		r.log.Warn("记录博主同步错误失败", "account", accountID, "err", err)
	}
}

// syncSocialAccount 同步一个博主：先刷资料，再翻页抓帖子。
//
// full（首次回填 / 手动「重新回填」）从断点游标继续翻到没页为止（每次最多 5 页）；
// auto 只抓第 1 页，遇到已入库的帖子即停 —— 这正是「每 6 小时一轮、不重复抓」的做法。
func (r *Runner) syncSocialAccount(ctx context.Context, c *Ctl, acc *store.SocialAccount, mode string) error {
	ad, ok := r.social.Get(acc.Platform)
	if !ok {
		return fmt.Errorf("平台未注册: %s", acc.Platform)
	}
	full := mode == "full" || !acc.BackfillDone

	// 1) 资料：失败不阻塞抓图（用户主要是为了看图），但要把原因留给用户看。
	if prof, err := ad.GetProfile(ctx, acc.Handle); err == nil && prof != nil {
		if prof.DisplayName == "" {
			prof.DisplayName = acc.DisplayName
		}
		if _, err := r.st.UpsertSocialAccount(&store.SocialAccount{
			Platform:    acc.Platform,
			Handle:      acc.Handle,
			DisplayName: prof.DisplayName,
			AvatarURL:   prof.AvatarURL,
			ProfileURL:  prof.ProfileURL,
		}); err != nil {
			r.log.Warn("更新博主资料失败", "account", acc.ID, "err", err)
		}
	} else if err != nil {
		if k := social.KindOf(err); k == social.KindRateLimited || k == social.KindNeedsAuth {
			return err
		}
		r.log.Warn("抓取博主资料失败，继续尝试抓帖子", "account", acc.ID, "err", err)
	}

	// 2) 帖子分页。
	cursor := ""
	if full {
		cursor = acc.Cursor // 回填断点
	}
	pages := 0
	newPhotos := 0
	backfillDone := !full
	for {
		if err := c.Check(); err != nil {
			return err
		}
		if pages >= socialPagesPerRun {
			break
		}
		page, err := ad.ListPosts(ctx, acc.Handle, cursor)
		if err != nil {
			if pages == 0 {
				return err
			}
			// 翻到一半失败：已经入库的先留着，下轮从断点接着来。
			r.log.Warn("分页抓取中断，保留已抓内容", "account", acc.ID, "page", pages+1, "err", err)
			break
		}
		if len(page.Posts) == 0 {
			backfillDone = true
			break
		}

		inputs := make([]store.SocialPostInput, 0, len(page.Posts))
		known := false
		for _, p := range page.Posts {
			if exists, err := r.st.HasSocialPost(acc.Platform, p.PostID); err == nil && exists && !full {
				known = true
				continue
			}
			photos := make([]store.SocialPhotoInput, 0, len(p.Photos))
			for _, ph := range p.Photos {
				photos = append(photos, store.SocialPhotoInput{URL: ph.URL, Width: ph.Width, Height: ph.Height, Seq: ph.Seq})
			}
			inputs = append(inputs, store.SocialPostInput{
				PostID: p.PostID, URL: p.URL, Caption: p.Caption, PostedAt: p.PostedAt, Photos: photos,
			})
		}

		ids, err := r.st.InsertSocialPosts(acc.ID, acc.Platform, inputs)
		if err != nil {
			return fmt.Errorf("写入帖子失败: %w", err)
		}
		newPhotos += len(ids)
		for _, id := range ids {
			r.hub.Publish("photo.new", map[string]any{
				"photoId":   id,
				"accountId": acc.ID,
				"platform":  acc.Platform,
				"handle":    acc.Handle,
			})
		}

		pages++
		cursor = page.NextCursor
		c.Progress(0, fmt.Sprintf("@%s 第 %d 页，新增 %d 张照片", acc.Handle, pages, newPhotos), 0, 0, 0)
		r.hub.Publish("social.progress", map[string]any{
			"accountId":   acc.ID,
			"handle":      acc.Handle,
			"page":        pages,
			"newPhotos":   newPhotos,
			"backfilling": full && !backfillDone,
		})

		if known {
			backfillDone = true
			break
		}
		if cursor == "" {
			backfillDone = true
			break
		}
	}

	if err := r.st.SaveSocialSync(acc.ID, cursor, backfillDone, "", time.Now()); err != nil {
		r.log.Warn("保存博主同步进度失败", "account", acc.ID, "err", err)
	}
	return nil
}

// ---- 存进本地库 ----

type socialSavePayload struct {
	PhotoIDs  []int64 `json:"photoIds"`
	LibraryID int64   `json:"libraryId"`
}

// EnqueueSocialSave 投递一次「照片存进本地库」任务。
func (r *Runner) EnqueueSocialSave(photoIDs []int64, libraryID int64) (int64, error) {
	return r.st.Enqueue(model.JobTypeSocialSave, socialSavePayload{PhotoIDs: photoIDs, LibraryID: libraryID}, 8)
}

// runSocialSaveJob 把选中的照片下载到本地库。
//
// 目录规则（docs/M5-SOCIAL.md §9）：
//
//	{库根}/{平台显示名}/{博主名}/照片/{YYYY-MM-DD}_{帖子 id 末 8 位}/{序号}.jpg
//
// 一条帖子一个目录，帖内按 seq 编号；顺便写一份 post.json 留正文与原帖地址。
func (r *Runner) runSocialSaveJob(ctx context.Context, j *model.Job, c *Ctl) error {
	var p socialSavePayload
	if err := json.Unmarshal([]byte(j.Payload), &p); err != nil {
		return fmt.Errorf("任务参数损坏")
	}
	if len(p.PhotoIDs) == 0 {
		return fmt.Errorf("没有选中照片")
	}
	lib, err := r.socialLibrary(p.LibraryID)
	if err != nil {
		return err
	}
	if err := library.EnsureWritable(lib.RootPath); err != nil {
		return fmt.Errorf("库目录不可写: %w", err)
	}
	client := r.httpClient("")

	saved := 0
	for i, photoID := range p.PhotoIDs {
		if err := c.Check(); err != nil {
			return err
		}
		photo, err := r.st.GetSocialPhotoByID(photoID)
		if err != nil {
			r.log.Warn("照片不存在，跳过", "photo", photoID, "err", err)
			continue
		}
		rel, size, err := r.saveSocialPhoto(ctx, client, lib, photo)
		if err != nil {
			r.log.Warn("保存照片失败", "photo", photoID, "err", err)
			c.Progress(float64(i+1)/float64(len(p.PhotoIDs))*100,
				fmt.Sprintf("第 %d/%d 张失败：%v", i+1, len(p.PhotoIDs), err), 0, 0, 0)
			continue
		}
		if err := r.st.SavePhotoFile(photoID, lib.ID, rel, size); err != nil {
			r.log.Warn("记录照片落盘失败", "photo", photoID, "err", err)
		}
		saved++
		c.Progress(float64(i+1)/float64(len(p.PhotoIDs))*100,
			fmt.Sprintf("已保存 %d/%d 张", saved, len(p.PhotoIDs)), 0, 0, 0)
	}
	r.hub.Publish("download.done", map[string]any{
		"kind":    "social_photo",
		"count":   saved,
		"library": lib.ID,
	})
	if saved == 0 {
		return fmt.Errorf("一张都没保存成功")
	}
	c.Progress(100, fmt.Sprintf("已保存 %d 张照片到「%s」", saved, lib.Name), 0, 0, 0)
	return nil
}

// socialLibrary 取目标库：payload 指定了就用它，否则用默认库。
func (r *Runner) socialLibrary(id int64) (*model.Library, error) {
	if id > 0 {
		lib, err := r.st.GetLibrary(id)
		if err != nil {
			return nil, fmt.Errorf("指定的本地库不存在")
		}
		return lib, nil
	}
	lib, err := r.st.DefaultLibrary()
	if err != nil {
		return nil, fmt.Errorf("还没有可用的本地库，请先在设置里创建")
	}
	return lib, nil
}

// saveSocialPhoto 下载一张照片并返回相对库根的路径与体积。
func (r *Runner) saveSocialPhoto(ctx context.Context, client *http.Client,
	lib *model.Library, photo *store.SocialPhotoRow) (string, int64, error) {
	rawURL := photo.URL
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "", 0, fmt.Errorf("图片地址无效")
	}

	blogger := strings.TrimSpace(photo.Account.DisplayName)
	if blogger == "" {
		blogger = photo.Account.Handle
	}
	postKey := postIDFromURL(photo.Post.URL)
	day := "undated"
	if photo.PostedAt != nil {
		day = photo.PostedAt.Format("2006-01-02")
	}
	dirRel := path.Join(
		platformName(photo.Platform),
		library.Sanitize(blogger, 60),
		"照片",
		day+"_"+tail(postKey, 8),
	)
	dirAbs := filepath.Join(lib.RootPath, filepath.FromSlash(dirRel))
	if !library.WithinRoot(lib.RootPath, dirAbs) {
		return "", 0, fmt.Errorf("目标路径越界")
	}
	if err := os.MkdirAll(dirAbs, 0o755); err != nil {
		return "", 0, fmt.Errorf("创建目录失败: %w", err)
	}

	ext := imageExt(u)
	name := fmt.Sprintf("%02d%s", maxInt(photo.Seq, 1), ext)
	fileAbs := filepath.Join(dirAbs, name)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	if ref := imgcache.RefererFor(u); ref != "" {
		req.Header.Set("Referer", ref)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("图床返回 %d", resp.StatusCode)
	}

	tmp := fileAbs + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", 0, err
	}
	size, err := io.Copy(f, resp.Body)
	cerr := f.Close()
	if err != nil {
		os.Remove(tmp)
		return "", 0, err
	}
	if cerr != nil {
		os.Remove(tmp)
		return "", 0, cerr
	}
	if size == 0 {
		os.Remove(tmp)
		return "", 0, fmt.Errorf("图床返回了空文件")
	}
	if err := os.Rename(tmp, fileAbs); err != nil {
		os.Remove(tmp)
		return "", 0, err
	}

	// post.json：一条帖子一份，留正文、原帖地址和「这帖存了哪几张」。
	//
	// 每次都重写（而不是「存在就跳过」）：同一条帖子的照片可能是分几次存的，
	// 只有重写才能让 photos 列表反映目录里真实存在的文件。
	meta := map[string]any{
		"platform": photo.Platform,
		"account":  photo.Account.Handle,
		"url":      photo.Post.URL,
		"caption":  photo.Post.Caption,
	}
	if photo.PostedAt != nil {
		meta["postedAt"] = photo.PostedAt.Format(time.RFC3339)
	}
	if files, err := savedPhotoFiles(dirAbs); err == nil {
		meta["photos"] = files
	}
	if b, err := json.MarshalIndent(meta, "", "  "); err == nil {
		if err := os.WriteFile(filepath.Join(dirAbs, "post.json"), b, 0o644); err != nil {
			r.log.Warn("写 post.json 失败", "dir", dirRel, "err", err)
		}
	}
	return path.Join(dirRel, name), size, nil
}

// postIDFromURL 从帖子地址里取平台侧的 id（`…/status/123` → 123，`/p/ABC/` → ABC）。
func postIDFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) == 0 {
		return ""
	}
	return segs[len(segs)-1]
}

func tail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// imageExt 猜图片扩展名；IG 的 CDN 地址没有后缀，默认 jpg。
func imageExt(u *url.URL) string {
	ext := strings.ToLower(path.Ext(u.Path))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
		return ext
	}
	return ".jpg"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// savedPhotoFiles 列出帖子目录里已经存好的图片文件名（按序号排序）。
//
// 只认「两位序号 + 图片后缀」这一种命名：`.part` 半成品、post.json、以及别的东西都不会被算进来。
func savedPhotoFiles(dirAbs string) ([]string, error) {
	entries, err := os.ReadDir(dirAbs)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) < 6 || name[0] < '0' || name[0] > '9' || name[1] < '0' || name[1] > '9' || name[2] != '.' {
			continue
		}
		switch strings.ToLower(path.Ext(name)) {
		case ".jpg", ".jpeg", ".png", ".webp", ".gif":
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}
