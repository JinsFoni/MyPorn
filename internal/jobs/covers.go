package jobs

import (
	"context"
	"strings"
	"time"

	"github.com/mario/myporn/internal/store"
)

// 封面预热与修复。
//
// 为什么需要它：pornhub 的封面地址（`pix-*.phncdn.com/.../rs:fit:320:180`）**带签名**，
// 有效期一天左右，过期后再抓就是 472。而浏览器那头是懒加载的——你隔两天才点开某部片，
// 地址早就死了，封面就永远空白。所以必须在「刚解析到、地址还新鲜」的那一小段时间里
// 把图抓进本地缓存，之后就再也不依赖签名了。
//
// 两个入口：
//   - 同步时：新片一入库就立刻预热（窗口最新鲜）；
//   - 启动后/每轮同步：扫一遍「封面没进缓存」的影片补上，顺手把地址换成详情页的新签名。

const (
	// coverWarmPerSync 一次同步最多预热多少张封面：增量同步一般新增几部，回填时再多也不该
	// 在同步里堆一长串请求，剩下的交给修复扫。
	coverWarmPerSync = 40
	// coverRepairScan 每轮修复看多少条影片记录（命中缓存只是 stat，很便宜）。
	coverRepairScan = 400
	// coverRepairLimit 每轮修复最多真去抓/换地址多少张，别把上游和弱 CPU 打满
	//（冷启动清一色没缓存时，60 张 ≈ 十秒量级；剩下的留给下一轮和「点开时懒加载」）。
	coverRepairLimit = 60
	// coverRepairTimeout 一轮修复的总时长上限。
	coverRepairTimeout = 3 * time.Minute
)

// warmCovers 在后台把刚解析到的封面地址抓进本地缓存（尽力而为，失败只记日志）。
func (r *Runner) warmCovers(tasks []store.VideoThumb) {
	if r.img == nil || len(tasks) == 0 {
		return
	}
	if len(tasks) > coverWarmPerSync {
		tasks = tasks[:coverWarmPerSync]
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		r.warmCoverList(ctx, tasks)
	}()
}

// warmCoverList 是 warmCovers 的同步版本（测试直接调它）。
func (r *Runner) warmCoverList(ctx context.Context, tasks []store.VideoThumb) int {
	warmed := 0
	for _, task := range tasks {
		if ctx.Err() != nil {
			return warmed
		}
		if r.img.Warm(ctx, task.Platform, task.ThumbURL) {
			warmed++
			continue
		}
		// 抓不到就算了：修复扫会拿详情页的新地址再试一次。
		r.log.Debug("封面预热失败", "video", task.ID, "url", task.ThumbURL)
	}
	return warmed
}

// repairInBackground 起一轮后台修复（同一时刻只跑一轮）。
func (r *Runner) repairInBackground() {
	if r.img == nil || !r.repairing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer r.repairing.Store(false)
		r.RepairCovers(context.Background())
	}()
}

// RepairCovers 扫一遍封面没进缓存的影片，补抓；抓不到的就用详情页换一个新签名地址再抓。
// 返回本轮补好的张数。串行执行，靠 coverRepairLimit 控制强度。
//
// 顺序有讲究：**带签名（会过期）的地址排在前面**。它们是唯一「错过这个村就没这个店」的一类——
// 缓存里有就永远没事，缓存一旦没了，只有趁详情页还能给出新链接时才救得回来。
func (r *Runner) RepairCovers(ctx context.Context) int {
	if r.img == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, coverRepairTimeout)
	defer cancel()

	urgent, normal, err := r.scanCoverRows()
	if err != nil {
		r.log.Warn("读影片封面列表失败", "err", err)
		return 0
	}

	var (
		scanned   int
		fixed     int
		attempted int
	)
	for _, bucket := range [][]store.VideoThumb{urgent, normal} {
		for _, row := range bucket {
			if ctx.Err() != nil || attempted >= coverRepairLimit {
				goto done
			}
			scanned++
			if r.img.Cached(row.Platform, row.ThumbURL) {
				continue
			}
			attempted++
			if r.img.Warm(ctx, row.Platform, row.ThumbURL) {
				fixed++
				continue
			}
			// 抓不到：多半是签名过期了，重新解析详情页换一个新地址。
			if fresh := r.refreshCoverURL(ctx, row); fresh != "" && r.img.Warm(ctx, row.Platform, fresh) {
				fixed++
			}
		}
	}
done:
	if fixed > 0 {
		r.log.Info("封面修复完成", "补上", fixed, "检查", scanned)
	}
	return fixed
}

// scanCoverRows 按 id 倒序扫一批影片，分成「带签名的」和「其他的」两桶。
func (r *Runner) scanCoverRows() (urgent, normal []store.VideoThumb, err error) {
	var before int64
	for len(urgent)+len(normal) < coverRepairScan {
		rows, err := r.st.ListVideoThumbs(before, 50)
		if err != nil {
			return nil, nil, err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			before = row.ID
			if signedThumb(row.ThumbURL) {
				urgent = append(urgent, row)
			} else {
				normal = append(normal, row)
			}
		}
		if len(rows) < 50 {
			break
		}
	}
	return urgent, normal, nil
}

// signedThumb 判断封面地址是不是「带签名、会过期」的那种（pornhub 的 pix 图床）。
func signedThumb(u string) bool {
	return strings.Contains(u, "hdnea=") || strings.Contains(u, "validto=") || strings.Contains(u, "//pix-")
}

// refreshCoverURL 用详情页重新解析封面地址，变了就写回数据库并返回新地址。
func (r *Runner) refreshCoverURL(ctx context.Context, row store.VideoThumb) string {
	ad, ok := r.reg.Get(row.Platform)
	if !ok {
		return ""
	}
	v, err := ad.GetVideo(ctx, row.PlatformVideoID)
	if err != nil || v == nil || v.ThumbURL == "" {
		r.log.Debug("换封面地址失败", "video", row.ID, "err", err)
		return ""
	}
	if v.ThumbURL == row.ThumbURL {
		// 地址没变，再抓也是同一个结果，别每轮都重试同一张。
		return ""
	}
	if err := r.st.UpdateVideoThumb(row.ID, v.ThumbURL); err != nil {
		r.log.Warn("封面地址写回失败", "video", row.ID, "err", err)
		return ""
	}
	return v.ThumbURL
}
