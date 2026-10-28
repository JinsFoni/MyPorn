// Command myporn 是 MyPorn 服务端入口（web + worker + scheduler 同进程）。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mario/myporn/internal/config"
	"github.com/mario/myporn/internal/events"
	"github.com/mario/myporn/internal/httpapi"
	"github.com/mario/myporn/internal/imgcache"
	"github.com/mario/myporn/internal/jobs"
	"github.com/mario/myporn/internal/platform"
	"github.com/mario/myporn/internal/platform/beeg"
	"github.com/mario/myporn/internal/platform/bilibili"
	"github.com/mario/myporn/internal/platform/pornhub"
	"github.com/mario/myporn/internal/platform/xcom"
	"github.com/mario/myporn/internal/platform/xhamster"
	"github.com/mario/myporn/internal/proxying"
	"github.com/mario/myporn/internal/secret"
	"github.com/mario/myporn/internal/social"
	"github.com/mario/myporn/internal/social/igram"
	socialx "github.com/mario/myporn/internal/social/xcom"
	"github.com/mario/myporn/internal/store"
	"github.com/mario/myporn/internal/webui"
	"github.com/mario/myporn/internal/ytsub"
)

func main() {
	var (
		root       = flag.String("root", ".", "项目根目录（读取 .env.local 与相对路径）")
		addr       = flag.String("addr", "", "监听地址，默认 :$PORT")
		debugParse = flag.String("debug-parse", "", "调试用：抓取并打印指定艺人主页的解析结果")
		debugVideo = flag.String("debug-video", "", "调试用：抓取并打印指定 viewkey 的播放解析结果")
		backfill   = flag.Bool("backfill-meta", false, "补齐历史影片的详情元数据（标签、简介等），补完即退出")
		backfillAv = flag.Bool("backfill-avatar", false, "给没有头像的艺人从站外头像源（beeg、X）补一张，补完即退出")
		backfillPf = flag.Bool("backfill-profile", false, "把 beeg / xHamster 的艺人资料整份换成 Pornhub 上的，换完即退出")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// 把工作目录切到项目根，保证 ./data、./media 等相对路径稳定。
	if abs, err := filepath.Abs(*root); err == nil {
		if err := os.Chdir(abs); err != nil {
			logger.Error("切换工作目录失败", "err", err)
			os.Exit(1)
		}
	}

	cfg, err := config.Load(*root)
	if err != nil {
		logger.Error("加载配置失败", "err", err)
		os.Exit(1)
	}
	if *addr != "" {
		cfg.Port = *addr
	}

	box, err := secret.New(cfg.SecretKey)
	if err != nil {
		logger.Error("初始化加密器失败", "err", err)
		os.Exit(1)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		logger.Error("打开数据库失败", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	// 代理解析器只建一次：所有 HTTP 客户端都通过它取代理，于是「设置里改代理」即时生效。
	px := proxying.New(st, cfg)
	reg, err := buildRegistry(cfg, st, box, logger, px)
	if err != nil {
		logger.Error("初始化平台适配器失败", "err", err)
		os.Exit(1)
	}

	avatars := buildAvatarRegistry(cfg, logger, px)
	profiles := buildProfileRegistry(reg, logger)

	if *debugParse != "" {
		runDebugParse(reg, *debugParse)
		return
	}
	if *debugVideo != "" {
		runDebugVideo(reg, *debugVideo)
		return
	}
	if *backfill {
		runBackfillMeta(st, reg, logger)
		return
	}
	if *backfillAv {
		runBackfillAvatar(st, avatars, logger)
		return
	}
	if *backfillPf {
		runBackfillProfile(st, reg, profiles, logger)
		return
	}

	socialReg, err := buildSocialRegistry(cfg, st, box, logger, px)
	if err != nil {
		logger.Error("初始化社交适配器失败", "err", err)
		os.Exit(1)
	}

	// YouTube 订阅（M6）：yt-dlp 调用器 + 按 IP 计的限速器。
	// 代理 / Cookie / 格式串都是每次调用现取的，所以设置里改完立刻生效。
	ytDlp := ytsub.NewYtDlp(
		func() string {
			if v, err := st.GetSetting("ytdlp_path"); err == nil && strings.TrimSpace(v) != "" {
				return v
			}
			return cfg.YtDlpPath
		},
		proxyFunc(px, "youtube"),
		func() string {
			v, err := st.GetSecret(box, "yt_cookies")
			if err != nil {
				return ""
			}
			return v
		},
		func() string {
			v, _ := st.GetSetting("yt_format")
			return v
		},
		logger)
	ytThrottle := social.NewThrottle(1200 * time.Millisecond)

	hub := events.NewHub()
	// 图片缓存服务端一份：HTTP 代理与同步任务共用同一份磁盘缓存（同步时预热封面）。
	images := imgcache.New(cfg.DataDir, px.Func(), cfg.FFmpegPath, logger)
	// 图片缓存上限：照片墙会持续往缓存里堆图（一条帖子就好几张），必须有个天花板。
	images.SetMaxBytes(int64(cfg.ImgCacheMaxMB) << 20)
	go func() {
		if removed, freed := images.Sweep(); removed > 0 {
			logger.Info("启动时图片缓存超限，已淘汰旧文件",
				"files", removed, "freedMB", freed/(1<<20))
		}
	}()
	runner := jobs.NewRunner(cfg, st, reg, socialReg, avatars, profiles, hub, logger, images)
	// 必须在 RegisterDefaults 之前注入：注册表按「有没有 yt-dlp」决定要不要挂 YouTube 任务。
	runner.SetYouTube(ytDlp, ytThrottle)
	runner.RegisterDefaults()

	// yt-dlp 是这个功能最脆的一环（实测旧版全线 403，见 M6 §11.5），启动时先把版本打进日志。
	verCtx, cancelVer := context.WithTimeout(context.Background(), 15*time.Second)
	ytVersion := ytDlp.Version(verCtx)
	cancelVer()
	if v := ytVersion; !v.Found {
		logger.Warn("没有找到 yt-dlp：YouTube 只能抓 RSS（没有时长、没有深度回填），也不能下载")
	} else if v.Stale {
		logger.Warn("yt-dlp 版本偏旧，下载很可能失败", "version", v.Version, "ageDays", v.AgeDays)
	} else {
		logger.Info("yt-dlp 可用", "version", v.Version, "path", v.Path)
	}

	// ffmpeg 只在下载任务的封装步骤用到；缺失不影响浏览与同步。
	checkCtx, cancelCheck := context.WithTimeout(context.Background(), 15*time.Second)
	if err := runner.Encoder().Check(checkCtx); err != nil {
		logger.Warn("ffmpeg 不可用，下载任务的封装步骤会失败", "err", err)
	} else {
		logger.Info("ffmpeg 可用", "path", cfg.FFmpegPath)
	}
	cancelCheck()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go runner.Start(ctx)
	go runScheduler(ctx, st, runner, logger)
	go runSocialScheduler(ctx, st, runner, logger)
	go runYtScheduler(ctx, st, runner, logger)
	// 启动后补一轮封面（历史数据里可能存着带签名、早就过期的封面地址）。
	go func() {
		if n := runner.RepairCovers(ctx); n > 0 {
			logger.Info("启动封面修复完成", "张数", n)
		}
	}()

	srv := httpapi.New(cfg, st, reg, socialReg, hub, runner, box, logger, webui.FS(), images)
	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel2()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	logger.Info("MyPorn 已启动", "addr", httpServer.Addr, "db", cfg.DBPath, "library", cfg.LibraryRoot)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("HTTP 服务异常退出", "err", err)
		os.Exit(1)
	}
	logger.Info("已停止")
}

// buildSocialRegistry 构造社交动态（M5）的适配器。
//
// 两个站出网各用各的（可分别挂代理）；Instagram 需要登录态，适配器每次请求现读设置，
// 用户改完 Cookie 立刻生效，不用重启。
func buildSocialRegistry(cfg *config.Config, st *store.Store, box *secret.Box, log *slog.Logger,
	px *proxying.Resolver) (*social.Registry, error) {
	reg := social.NewRegistry()

	// 设置读取：Cookie 这类敏感值存的是密文，读不到再退回明文（兼容老数据）。
	setting := func(key string) string {
		if v, err := st.GetSecret(box, key); err == nil && v != "" {
			return v
		}
		v, _ := st.GetSetting(key)
		return v
	}

	xClient, err := platform.NewDynamicHTTPClient(proxyFunc(px, "x"), 25*time.Second)
	if err != nil {
		log.Warn("X 客户端初始化失败，改用直连", "err", err)
		xClient = nil
	}
	// 注意：internal/platform/xcom 是「借 X 头像」的适配器，internal/social/xcom 是「抓帖子照片」的，
	// 两个包同名，这里用 socialx 别名区分。
	reg.Register(socialx.New(xClient))

	igClient, err := platform.NewDynamicHTTPClient(proxyFunc(px, "instagram"), 25*time.Second)
	if err != nil {
		log.Warn("Instagram 客户端初始化失败，改用直连", "err", err)
		igClient = nil
	}
	reg.Register(igram.New(igClient, setting))

	return reg, nil
}

// runSocialScheduler 每 6 小时（可在设置里改）把一轮「全部博主同步」丢进队列。
// 这里不按博主算「到期」：两个站的限流都是按 IP 的，集中成一轮串行抓取比错开更可控。
func runSocialScheduler(ctx context.Context, st *store.Store, runner *jobs.Runner, log *slog.Logger) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	var lastRun time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		hours := 6
		if v, err := st.GetSetting("social_sync_hours"); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				hours = n
			}
		}
		if time.Since(lastRun) < time.Duration(hours)*time.Hour {
			continue
		}
		accounts, err := st.ListSocialAccounts(true)
		if err != nil || len(accounts) == 0 {
			lastRun = time.Now()
			continue
		}
		if _, err := runner.EnqueueSocialSync(0, "auto"); err != nil {
			log.Warn("投递社交动态同步失败", "err", err)
			continue
		}
		lastRun = time.Now()
	}
}

// runYtScheduler 每 yt_sync_hours（默认 6）把一轮「全部频道增量同步」丢进队列。
// 与社交动态同理：YouTube 也按 IP 计限流，集中成一轮串行抓比错开更可控。
func runYtScheduler(ctx context.Context, st *store.Store, runner *jobs.Runner, log *slog.Logger) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	var lastRun time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		hours := 6
		if v, err := st.GetSetting("yt_sync_hours"); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				hours = n
			}
		}
		if time.Since(lastRun) < time.Duration(hours)*time.Hour {
			continue
		}
		channels, err := st.ListYtChannels(true)
		if err != nil || len(channels) == 0 {
			lastRun = time.Now()
			continue
		}
		if _, err := runner.EnqueueYtSync(0, "auto"); err != nil {
			log.Warn("投递 YouTube 同步失败", "err", err)
			continue
		}
		lastRun = time.Now()
	}
}

// proxyFunc 把代理解析器绑到某个平台，得到「每次发请求现问」的取值函数。
func proxyFunc(px *proxying.Resolver, platformKey string) func() string {
	return func() string { return px.For(platformKey) }
}

// buildRegistry 按配置构造各平台适配器。
func buildRegistry(cfg *config.Config, st *store.Store, box *secret.Box, log *slog.Logger,
	px *proxying.Resolver) (*platform.Registry, error) {
	reg := platform.NewRegistry()

	cookie, err := st.GetSecret(box, "pornhub_cookie")
	if err != nil {
		log.Warn("读取 Pornhub Cookie 失败", "err", err)
	}
	baseURL, _ := st.GetSetting("pornhub_base_url")
	if baseURL == "" {
		baseURL = "https://cn.pornhub.com"
	}
	ph, err := pornhub.New(baseURL, proxyFunc(px, "pornhub"), cookie)
	if err != nil {
		return nil, fmt.Errorf("初始化 Pornhub 适配器: %w", err)
	}
	reg.Register(ph)
	if err := st.SeedPlatform(ph.Key(), ph.Name(), baseURL); err != nil {
		log.Warn("写入平台信息失败", "err", err)
	}

	xhBase, _ := st.GetSetting("xhamster_base_url")
	if xhBase == "" {
		xhBase = "https://xhamster.com"
	}
	xh, err := xhamster.New(xhBase, proxyFunc(px, "xhamster"))
	if err != nil {
		return nil, fmt.Errorf("初始化 xHamster 适配器: %w", err)
	}
	reg.Register(xh)
	if err := st.SeedPlatform(xh.Key(), xh.Name(), xhBase); err != nil {
		log.Warn("写入平台信息失败", "err", err)
	}

	bgBase, _ := st.GetSetting("beeg_base_url")
	if bgBase == "" {
		bgBase = "https://beeg.com"
	}
	bg, err := beeg.New(bgBase, proxyFunc(px, "beeg"))
	if err != nil {
		return nil, fmt.Errorf("初始化 beeg 适配器: %w", err)
	}
	reg.Register(bg)
	if err := st.SeedPlatform(bg.Key(), bg.Name(), bgBase); err != nil {
		log.Warn("写入平台信息失败", "err", err)
	}

	blBase, _ := st.GetSetting("bilibili_base_url")
	if blBase == "" {
		blBase = "https://www.bilibili.com"
	}
	// Cookie 现取：设置页填完 SESSDATA，下一次解析就用上（1080P 以上必须登录）。
	bl, err := bilibili.New(blBase, proxyFunc(px, "bilibili"), func() string {
		v, gerr := st.GetSecret(box, "bili_cookie")
		if gerr != nil {
			return ""
		}
		return v
	})
	if err != nil {
		return nil, fmt.Errorf("初始化 B 站适配器: %w", err)
	}
	reg.Register(bl)
	if err := st.SeedPlatform(bl.Key(), bl.Name(), blBase); err != nil {
		log.Warn("写入平台信息失败", "err", err)
	}
	return reg, nil
}

// buildAvatarRegistry 构造站外头像来源，按顺序依次尝试，命中即止：
//  1. beeg —— 有按名字查的公开接口，返回站内自用的那张头像，最准；
//  2. X —— 有些艺人在 beeg 上查得到人却没照片，但 X 上有主页。
//
// Pornhub 的头像藏在页面 DOM 里（改版就抓不到），所以不参与跨站补齐。
func buildAvatarRegistry(cfg *config.Config, log *slog.Logger, px *proxying.Resolver) *platform.AvatarRegistry {
	avatars := platform.NewAvatarRegistry()
	if client, err := platform.NewDynamicHTTPClient(proxyFunc(px, "beeg"), 15*time.Second); err != nil {
		log.Warn("初始化 beeg 头像客户端失败，跳过这个来源", "err", err)
	} else {
		avatars.Register(beeg.NewAvatarProvider(client))
	}
	if client, err := platform.NewDynamicHTTPClient(proxyFunc(px, "x"), 15*time.Second); err != nil {
		log.Warn("初始化 X 头像客户端失败，跳过这个来源", "err", err)
	} else {
		avatars.Register(xcom.NewAvatarProvider(client))
	}
	return avatars
}

// buildProfileRegistry 构造站外资料来源。目前只有 Pornhub 一家：
// 它的艺人档案最全（简介、资料标签、三围、社交链接、排名都有），
// beeg / xHamster 的艺人都从它这儿借资料。
func buildProfileRegistry(reg *platform.Registry, log *slog.Logger) *platform.ProfileRegistry {
	profiles := platform.NewProfileRegistry()
	a, ok := reg.Get("pornhub")
	if !ok {
		return profiles
	}
	provider, ok := a.(platform.ProfileProvider)
	if !ok {
		log.Warn("Pornhub 适配器不支持资料源接口，跳过")
		return profiles
	}
	profiles.Register(provider)
	return profiles
}

// runBackfillProfile 把各平台的艺人资料整份换成 Pornhub 上的（名字对不上就不动）。
// 一次性返工用：老数据的资料是各站自己那点内容，换过之后不用再跑。
func runBackfillProfile(st *store.Store, reg *platform.Registry, profiles *platform.ProfileRegistry, log *slog.Logger) {
	filler := jobs.NewProfileFiller(st, profiles, log)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	checked, filled := 0, 0
	for _, a := range reg.All() {
		if a.Key() == "pornhub" {
			continue // 本来就是 Pornhub 的艺人，没什么可借
		}
		items, err := st.ListPerformersByPlatform(a.Key())
		if err != nil {
			log.Error("读取艺人失败", "platform", a.Key(), "err", err)
			continue
		}
		for _, p := range items {
			if ctx.Err() != nil {
				fmt.Printf("补资料中断：%v\n", ctx.Err())
				return
			}
			checked++
			if filler.Fill(ctx, p) {
				filled++
			}
		}
	}
	fmt.Printf("补资料完成：检查 %d 位，换资料 %d 位\n", checked, filled)
}

// runBackfillAvatar 一次性给没有头像的艺人补头像。
func runBackfillAvatar(st *store.Store, avatars *platform.AvatarRegistry, log *slog.Logger) {
	filler := jobs.NewAvatarFiller(st, avatars, log)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	checked, filled, err := filler.FillMissing(ctx)
	if err != nil {
		fmt.Printf("补齐头像中断：%v\n", err)
	}
	fmt.Printf("补齐完成：检查 %d 位，写入 %d 位\n", checked, filled)
}

// runScheduler 每分钟扫描到期订阅并投递增量同步任务。
func runScheduler(ctx context.Context, st *store.Store, runner *jobs.Runner, log *slog.Logger) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		ids, err := st.DueSubscriptions(20)
		if err != nil {
			log.Warn("查询到期订阅失败", "err", err)
			continue
		}
		for _, id := range ids {
			active, err := st.HasActiveSyncJob(id)
			if err != nil || active {
				continue
			}
			if _, err := runner.EnqueueSync(id, "auto"); err != nil {
				log.Warn("投递同步任务失败", "subscription", id, "err", err)
			}
		}
	}
}

func runDebugParse(reg *platform.Registry, rawURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, a := range reg.All() {
		id, err := a.ParsePerformerURL(rawURL)
		if err != nil {
			continue
		}
		fmt.Printf("平台: %s  标识: %s\n", a.Key(), id)
		p, err := a.GetPerformer(ctx, id)
		if err != nil {
			fmt.Println("抓取艺人失败:", err)
			return
		}
		fmt.Printf("艺人: %q 头像=%s 视频数=%s 订阅数=%s\n", p.Name, p.AvatarURL, countText(p.VideoCount), countText(p.SubscriberCount))
		if p.Bio != "" {
			fmt.Printf("简介: %s\n", strings.ReplaceAll(p.Bio, "\n", " / "))
		}
		if !p.Body.Empty() {
			fmt.Printf("身材: %dcm B%d W%d H%d\n", p.Body.HeightCm, p.Body.Bust, p.Body.Waist, p.Body.Hips)
		}
		if p.Rank > 0 {
			fmt.Printf("排名: %d\n", p.Rank)
		}
		for _, f := range p.Profile {
			fmt.Printf("资料: %s: %s\n", f.Label, f.Value)
		}
		for _, l := range p.Social {
			fmt.Printf("链接: %s → %s\n", l.Label, l.URL)
		}
		page, err := a.ListPerformerVideos(ctx, id, 1)
		if err != nil {
			fmt.Println("抓取影片列表失败:", err)
			return
		}
		fmt.Printf("第 1 页：%d 条，总页数=%d，还有更多=%v\n", len(page.Items), page.TotalPages, page.HasMore)
		for i, v := range page.Items {
			if i >= 5 {
				break
			}
			fmt.Printf("  - [%s] %q 时长=%ds 缩略图=%s\n", v.PlatformVideoID, v.Title, v.DurationSec, v.ThumbURL)
		}
		return
	}
	fmt.Println("无法识别该链接所属平台")
}

// runBackfillMeta 给「只有列表页信息」的老影片补详情页元数据（标签、简介）。
// 标签只在详情页里有，所以历史数据要补一次；补过之后新影片在播放解析时顺带就填上了。
func runBackfillMeta(st *store.Store, reg *platform.Registry, logger *slog.Logger) {
	ctx := context.Background()
	total, filled := 0, 0
	items, err := st.ListVideosMissingMeta(200)
	if err != nil {
		logger.Error("读取待补影片失败", "err", err)
		return
	}
	for _, v := range items {
		ad, ok := reg.Get(v.Platform)
		if !ok {
			continue
		}
		total++
		meta, gerr := ad.GetVideo(ctx, v.PlatformVideoID)
		if gerr != nil {
			logger.Warn("拉取详情失败", "video", v.ID, "err", gerr)
			continue
		}
		if len(meta.Tags) == 0 && meta.Description == "" {
			continue
		}
		if serr := st.FillVideoMeta(v.ID, meta); serr != nil {
			logger.Warn("写入元数据失败", "video", v.ID, "err", serr)
			continue
		}
		filled++
		fmt.Printf("[%d] %s -> %d 个标签\n", v.ID, shortTitle(v.Title), len(meta.Tags))
	}
	fmt.Printf("补齐完成：检查 %d 部，写入 %d 部\n", total, filled)
}

// countText 把可空的计数字段打成可读文本（nil 显示 -）。
func countText(n *int) string {
	if n == nil {
		return "-"
	}
	return strconv.Itoa(*n)
}

func shortTitle(s string) string {
	r := []rune(s)
	if len(r) > 18 {
		return string(r[:18]) + "…"
	}
	return s
}

func runDebugVideo(reg *platform.Registry, viewkey string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, a := range reg.All() {
		v, err := a.GetVideo(ctx, viewkey)
		if err != nil {
			fmt.Printf("[%s] 解析失败: %v\n", a.Key(), err)
			continue
		}
		fmt.Printf("[%s] 标题=%q 时长=%ds 标签=%v\n", a.Key(), v.Title, v.DurationSec, v.Tags)
		vs, err := a.ResolveStreams(ctx, viewkey)
		if err != nil {
			fmt.Println("解析播放地址失败:", err)
			return
		}
		for _, vv := range vs {
			fmt.Printf("  %dp bw=%d codec=%s %s\n", vv.Height, vv.Bandwidth, vv.Codec, vv.URL)
		}
		return
	}
}
