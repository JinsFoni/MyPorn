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
	"strings"
	"time"

	"github.com/JinsFoni/MyPorn/internal/imgcache"
	"github.com/JinsFoni/MyPorn/internal/library"
	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/social"
	"github.com/JinsFoni/MyPorn/internal/store"
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
	discovered, skipped := 0, 0 // 仅 discover 轮用：真跑了几个 / 跳过了几个不支持发现轮的
	for i, acc := range accounts {
		if err := c.Check(); err != nil {
			return err
		}
		label := fmt.Sprintf("%s/%d", social.PlatformNames()[acc.Platform], i+1)
		c.Progress(float64(i)/float64(len(accounts))*100,
			fmt.Sprintf("同步 %s @%s（%s）", platformName(acc.Platform), acc.Handle, label), 0, 0, 0)

		var err error
		if p.Mode == "discover" {
			// discover 轮只跑便宜的那条路子，且只跑支持它的平台（目前是 X）。
			ad, ok := r.social.Get(acc.Platform)
			if !ok {
				continue
			}
			if _, can := ad.(social.Discoverer); !can {
				skipped++
				continue
			}
			discovered++
			err = r.discoverSocialAccount(ctx, c, acc)
		} else {
			err = r.syncSocialAccount(ctx, c, acc, p.Mode)
		}
		if err != nil {
			failed++
			r.recordSocialError(acc.ID, err)
			// 抓取失败不该拖住已经抓到的图（以及以前漏下的）：落盘是本地活，跟限流/风控无关。
			r.enqueuePhotoSave(acc.ID, nil)
			// 未登录会让整轮都白跑，直接收工；限流不再收工——X 的额度是按宿主分的
			// （widget / 主页 / 单帖各一份，§15.15），一个账号撞上的往往只挡住它自己，
			// 后面的账号换个宿主照样能抓到新帖。真的整站限流时，Throttle 会让后续请求
			// 立刻失败、不产生流量，继续跑也没有代价。
			if social.KindOf(err) == social.KindNeedsAuth {
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
	if p.Mode == "discover" {
		// 跳过的是「没有便宜发现路径」的平台（Instagram），它们归完整同步管，别说成失败。
		c.Progress(100, fmt.Sprintf("发现轮完成：%d 个博主（跳过 %d 个）", discovered, skipped), 0, 0, 0)
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

// storeSocialPage 把一页帖子写进库，返回新增照片 id、以及「这一页有没有已入库的帖子」。
//
// 返回的是 id 而不是数量：新增的照片紧接着要自动落盘（2026-11-04 用户要求
// 「图片抓取后自动下载入库」），ids 直接进存图任务的 payload，不再让用户勾选。
// skipKnown=true 时遇到已入库的帖子就跳过并标记 known（增量同步靠它提前收手）；
// false 用于回填，重复的帖子照样重写一遍（幂等，顺手补全缺失字段）。
func (r *Runner) storeSocialPage(acc *store.SocialAccount, page *social.Page, skipKnown bool) ([]int64, bool, error) {
	inputs := make([]store.SocialPostInput, 0, len(page.Posts))
	known := false
	for _, p := range page.Posts {
		if exists, err := r.st.HasSocialPost(acc.Platform, p.PostID); err == nil && exists && skipKnown {
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
		return nil, known, fmt.Errorf("写入帖子失败: %w", err)
	}
	for _, id := range ids {
		r.hub.Publish("photo.new", map[string]any{
			"photoId":   id,
			"accountId": acc.ID,
			"platform":  acc.Platform,
			"handle":    acc.Handle,
		})
	}
	return ids, known, nil
}

// discoverSocialAccount 只跑便宜的那条发现路子（social.Discoverer），不刷资料、不碰 widget。
//
// 它跟完整同步的区别就是「多久跑一次」：X 主页 HTML 一页只给 5–8 个帖子 id，完整同步
// 6 小时一轮，两轮之间发超过这个数就滑走了（2026-11-04 实测，§15.15）。这条路每账号只花
// 1 次主页请求 + 每条 1 次单帖请求，也不占 widget 的 30 次/15 分钟额度，所以单独按小时跑。
// 不动回填游标（sync_cursor / backfill_done）——那是完整同步的进度；last_synced_at 由轮次
// 收尾的 recordSocialError(nil) 统一刷新（界面上的「上次同步」应当反映最近一次成功）。
func (r *Runner) discoverSocialAccount(ctx context.Context, c *Ctl, acc *store.SocialAccount) error {
	ad, ok := r.social.Get(acc.Platform)
	if !ok {
		return fmt.Errorf("平台未注册: %s", acc.Platform)
	}
	disc, ok := ad.(social.Discoverer)
	if !ok {
		return nil
	}
	page, err := disc.Discover(ctx, acc.Handle)
	if err != nil {
		return err
	}
	ids, _, err := r.storeSocialPage(acc, page, false)
	if err != nil {
		return err
	}
	n := len(ids)
	if n > 0 {
		// 发现轮不走 SaveSocialSync（那会动回填游标），所以计数得自己刷，否则界面上
		// 「37 帖 / 52 图」会一直停在完整同步那一刻。
		if err := r.st.RefreshSocialCounts(acc.ID); err != nil {
			r.log.Warn("刷新博主计数失败", "account", acc.ID, "err", err)
		}
		c.Progress(0, fmt.Sprintf("@%s 发现轮新增 %d 张照片", acc.Handle, n), 0, 0, 0)
		r.hub.Publish("social.progress", map[string]any{
			"accountId": acc.ID,
			"handle":    acc.Handle,
			"page":      1,
			"newPhotos": n,
		})
	}
	r.enqueuePhotoSave(acc.ID, ids)
	return nil
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
	newIDs := []int64{}
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

		ids, known, err := r.storeSocialPage(acc, page, !full)
		if err != nil {
			return err
		}
		newPhotos += len(ids)
		newIDs = append(newIDs, ids...)

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
	// 抓完就自动落盘：新抓的排前面，任务里再补这个博主以前漏下的（见 socialUnsavedPerRun）。
	// 没抓到新图也投一次——「上次没下完」正是需要这一轮补的场合。
	r.enqueuePhotoSave(acc.ID, newIDs)
	return nil
}

// enqueuePhotoSave 把刚抓到的照片交给存图任务；accountID>0 时任务顺带补这个博主的库存。
func (r *Runner) enqueuePhotoSave(accountID int64, photoIDs []int64) {
	if _, err := r.EnqueueSocialSave(photoIDs, accountID); err != nil {
		r.log.Warn("投递照片自动入库任务失败", "account", accountID, "err", err)
	}
}

// ---- 存进本地库 ----

type socialSavePayload struct {
	// PhotoIDs 是这一轮同步刚抓到的新照片（优先下）。
	PhotoIDs []int64 `json:"photoIds"`
	// AccountID > 0 时顺带补这个博主名下漏下的照片；-1 表示全部博主（改完图片库根目录时用）。
	AccountID int64 `json:"accountId"`
}

// photoRootLibraryID 是 social_photo_files.library_id 的占位值：0 = 设置里的图片库根目录。
// 照片不再挂在任何视频库下，见 0014_photo_root.sql。
const photoRootLibraryID int64 = 0

// socialUnsavedPerRun 是一次任务最多补多少张「以前漏下的」照片。
//
// 为什么要补：自动入库是 2026-11-04 才有的（此前要用户在墙上勾选），存量一张都没落盘
// （本机实测 973 张）；而且失败是常态——图床抽风、磁盘满、根目录还没填。只下「本轮新增」
// 的话旧图永远补不回来。给个上限是为了别让一次任务跑成几百张的长下载：每轮补一批，几轮就齐。
const socialUnsavedPerRun = 200

// EnqueueSocialSave 投递一次「照片存进图片库」任务。photoIDs 可以是空的（纯补漏）。
func (r *Runner) EnqueueSocialSave(photoIDs []int64, accountID int64) (int64, error) {
	return r.st.Enqueue(model.JobTypeSocialSave,
		socialSavePayload{PhotoIDs: photoIDs, AccountID: accountID}, 8)
}

// photoRoot 读「图片库根目录」设置（`photo_root`），空值回落到 {媒体库根}/photos。
func (r *Runner) photoRoot() string {
	if r.st != nil {
		if v, err := r.st.GetSetting("photo_root"); err == nil {
			if root := strings.TrimSpace(v); root != "" {
				return root
			}
		}
	}
	if r.cfg != nil && strings.TrimSpace(r.cfg.LibraryRoot) != "" {
		return filepath.Join(r.cfg.LibraryRoot, "photos")
	}
	return ""
}

// runSocialSaveJob 把照片下载进图片库。
//
// 目录规则（docs/M5-SOCIAL.md §9）：
//
//	{图片库根}/{名字}/{年}/{月}/{YYYY-MM-DD}_{帖子 id 末 8 位}_{序号}.jpg
//
// 「名字」= 博主显示名，空着才用 @handle；年/月取帖子发布时间，缺时间的老帖落到
// 「未标注/未标注」——不硬塞进当下这个月，那会让归档看起来像今天发的。
// 同一帖的文件名共享 `{日期}_{帖子末 8 位}` 前缀，正文/原帖地址写在同目录
// `{前缀}.post.json` 里（照片不再一帖一目录，清单只能从库里取，见 PostSavedPhotoPaths）。
func (r *Runner) runSocialSaveJob(ctx context.Context, j *model.Job, c *Ctl) error {
	var p socialSavePayload
	if err := json.Unmarshal([]byte(j.Payload), &p); err != nil {
		return fmt.Errorf("任务参数损坏")
	}
	root := r.photoRoot()
	if root == "" {
		return fmt.Errorf("还没有配置图片库根目录，请先在设置里填写")
	}
	if err := library.EnsureWritable(root); err != nil {
		return fmt.Errorf("图片库目录不可写: %w", err)
	}
	ids, err := r.socialPhotoQueue(p)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		c.Progress(100, "没有需要下载的照片", 0, 0, 0)
		return nil
	}

	client := r.httpClient("")
	saved := 0
	// 落成了哪些：广播给前端清「待存」角标（一批最多 200 个 int，SSE 扛得住）
	savedIDs := make([]int64, 0, 16)
	for i, photoID := range ids {
		if err := c.Check(); err != nil {
			return err
		}
		photo, err := r.st.GetSocialPhotoByID(photoID)
		if err != nil {
			r.log.Warn("照片不存在，跳过", "photo", photoID, "err", err)
			continue
		}
		rel, size, err := r.saveSocialPhoto(ctx, client, root, photo)
		if err != nil {
			r.log.Warn("保存照片失败", "photo", photoID, "err", err)
			c.Progress(float64(i+1)/float64(len(ids))*100,
				fmt.Sprintf("第 %d/%d 张失败：%v", i+1, len(ids), err), 0, 0, 0)
			continue
		}
		if err := r.st.SavePhotoFile(photoID, photoRootLibraryID, rel, size); err != nil {
			r.log.Warn("记录照片落盘失败", "photo", photoID, "err", err)
		}
		// 元数据在记账之后再写：清单是从库里取的，先写就会漏掉这一张。
		r.writePhotoPostMeta(filepath.Join(root, filepath.FromSlash(photoRelDir(photo))), photo)
		savedIDs = append(savedIDs, photo.ID)
		saved++
		c.Progress(float64(i+1)/float64(len(ids))*100,
			fmt.Sprintf("已保存 %d/%d 张", saved, len(ids)), 0, 0, 0)
	}
	r.hub.Publish("photo.saved", map[string]any{
		"ids":   savedIDs,
		"count": saved,
	})
	if saved == 0 {
		return fmt.Errorf("一张都没保存成功")
	}
	// 还有没下完的就接着排：单个任务最多 socialUnsavedPerRun 张（别让一个任务跑几十分钟），
	// 但存量可能有上千张（改完图片库根目录的头一轮就是）。只在「这一轮确实下成了」时续排——
	// 剩下的全是坏图时 saved=0，队列就停，不会变成空转的死循环。
	if p.AccountID != 0 && saved > 0 {
		scope := p.AccountID
		if scope < 0 {
			scope = 0 // 0 = 所有博主
		}
		if rest, err := r.st.ListUnsavedSocialPhotos(scope, 1); err == nil && len(rest) > 0 {
			if _, err := r.EnqueueSocialSave(nil, p.AccountID); err != nil {
				r.log.Warn("续排照片补漏任务失败", "err", err)
			}
		}
	}
	// 「第几轮补漏」对用户没意义，只说清落哪儿了。
	c.Progress(100, fmt.Sprintf("已保存 %d 张照片到图片库", saved), 0, 0, 0)
	return nil
}

// socialPhotoQueue 拼出这次要下的照片：本轮新抓的 + 漏下的（有上限），已落盘的跳过。
func (r *Runner) socialPhotoQueue(p socialSavePayload) ([]int64, error) {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(p.PhotoIDs))
	add := func(id int64) error {
		if id <= 0 || seen[id] {
			return nil
		}
		seen[id] = true
		ok, err := r.st.PhotoSaved(id)
		if err != nil {
			return err
		}
		if !ok {
			out = append(out, id)
		}
		return nil
	}
	for _, id := range p.PhotoIDs {
		if err := add(id); err != nil {
			return nil, err
		}
	}
	if p.AccountID != 0 {
		accountID := p.AccountID
		if accountID < 0 {
			accountID = 0 // 0 = 所有博主
		}
		rest, err := r.st.ListUnsavedSocialPhotos(accountID, socialUnsavedPerRun)
		if err != nil {
			return nil, err
		}
		for _, id := range rest {
			if err := add(id); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// photoRelDir 是这张照片该落的目录（相对图片库根）：{名字}/{年}/{月}。
func photoRelDir(photo *store.SocialPhotoRow) string {
	name := strings.TrimSpace(photo.Account.DisplayName)
	if name == "" {
		name = photo.Account.Handle
	}
	if name == "" {
		name = "未知博主"
	}
	year, month := "未标注", "未标注"
	if photo.PostedAt != nil {
		year = photo.PostedAt.Format("2006")
		month = photo.PostedAt.Format("01")
	}
	return path.Join(library.Sanitize(name, 60), year, month)
}

// photoBaseName 是文件名主干：{YYYY-MM-DD}_{帖子 id 末 8 位}_{序号}（无时间的老帖省掉日期）。
func photoBaseName(photo *store.SocialPhotoRow) string {
	parts := []string{}
	if photo.PostedAt != nil {
		parts = append(parts, photo.PostedAt.Format("2006-01-02"))
	}
	if key := tail(postIDFromURL(photo.Post.URL), 8); key != "" {
		parts = append(parts, key)
	}
	parts = append(parts, fmt.Sprintf("%02d", maxInt(photo.Seq, 1)))
	return strings.Join(parts, "_")
}

// saveSocialPhoto 下载一张照片，返回相对图片库根的路径与体积。
func (r *Runner) saveSocialPhoto(ctx context.Context, client *http.Client,
	root string, photo *store.SocialPhotoRow) (string, int64, error) {
	rawURL := photo.URL
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "", 0, fmt.Errorf("图片地址无效")
	}

	dirRel := photoRelDir(photo)
	dirAbs := filepath.Join(root, filepath.FromSlash(dirRel))
	if !library.WithinRoot(root, dirAbs) {
		return "", 0, fmt.Errorf("目标路径越界")
	}
	if err := os.MkdirAll(dirAbs, 0o755); err != nil {
		return "", 0, fmt.Errorf("创建目录失败: %w", err)
	}

	ext := imageExt(u)
	base := photoBaseName(photo)
	name := base + ext
	fileAbs := filepath.Join(dirAbs, name)
	// 同名文件已经在：要么是同一张（落盘记录丢了，重下），要么是同帖同序号的另一行。
	// 不覆盖，后缀自己的 id，两张都留得下。
	if _, statErr := os.Stat(fileAbs); statErr == nil {
		name = fmt.Sprintf("%s_%d%s", base, photo.ID, ext)
		fileAbs = filepath.Join(dirAbs, name)
	}

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

	return path.Join(dirRel, name), size, nil
}

// writePhotoPostMeta 写同目录的 `{日期}_{帖子末 8 位}.post.json`：正文 / 原帖地址 /
// 发布时间 / 博主 / 这帖已经落盘的那几张。每次落一张就重写一遍，清单才是当前真值。
func (r *Runner) writePhotoPostMeta(dirAbs string, photo *store.SocialPhotoRow) {
	key := tail(postIDFromURL(photo.Post.URL), 8)
	base := key
	if photo.PostedAt != nil {
		base = photo.PostedAt.Format("2006-01-02")
		if key != "" {
			base += "_" + key
		}
	}
	if base == "" {
		base = "post"
	}
	meta := map[string]any{
		"platform": photo.Platform,
		"account":  photo.Account.Handle,
		"url":      photo.Post.URL,
		"caption":  photo.Post.Caption,
	}
	if photo.PostedAt != nil {
		meta["postedAt"] = photo.PostedAt.Format(time.RFC3339)
	}
	if paths, err := r.st.PostSavedPhotoPaths(photo.Post.ID); err == nil && len(paths) > 0 {
		files := make([]string, 0, len(paths))
		for _, p := range paths {
			files = append(files, filepath.Base(filepath.FromSlash(p)))
		}
		meta["photos"] = files
	}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(filepath.Join(dirAbs, base+".post.json"), b, 0o644); err != nil {
		r.log.Warn("写帖子元数据失败", "dir", dirAbs, "err", err)
	}
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
