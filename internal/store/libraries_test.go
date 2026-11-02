package store

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/JinsFoni/MyPorn/internal/model"
)

func TestLibraryCRUDAndDefault(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()

	a, err := st.CreateLibrary(&model.Library{Name: "A", RootPath: filepath.Join(t.TempDir(), "a"), Enabled: true, IsDefault: true, UploadPolicy: model.PolicyLocal})
	if err != nil {
		t.Fatalf("创建库失败: %v", err)
	}
	bb, err := st.CreateLibrary(&model.Library{Name: "B", RootPath: filepath.Join(t.TempDir(), "b"), Enabled: true, IsDefault: true, UploadPolicy: model.PolicyLocal})
	if err != nil {
		t.Fatalf("创建库失败: %v", err)
	}

	def, err := st.DefaultLibrary()
	if err != nil || def.ID != bb {
		t.Fatalf("后建为默认的库应成为唯一默认，得到 %+v (err=%v)", def, err)
	}
	libs, err := st.ListLibraries()
	if err != nil || len(libs) != 2 {
		t.Fatalf("应有 2 个库，得到 %d", len(libs))
	}
	la, _ := st.GetLibrary(a)
	if la.IsDefault {
		t.Error("旧默认库应被取消默认")
	}

	la.Name = "A2"
	la.IsDefault = true
	la.NamingTemplate = "{date} - {title}"
	if err := st.UpdateLibrary(la); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	got, _ := st.GetLibrary(a)
	if got.Name != "A2" || !got.IsDefault || got.NamingTemplate != "{date} - {title}" {
		t.Fatalf("更新未生效: %+v", got)
	}
	def, _ = st.DefaultLibrary()
	if def.ID != a {
		t.Fatal("切换默认库失败")
	}

	if err := st.DeleteLibrary(bb); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := st.GetLibrary(bb); err != ErrNotFound {
		t.Fatalf("删除后应查不到，得到 %v", err)
	}
}

func TestLibraryFileUpsertAndUsage(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()

	libID, err := st.CreateLibrary(&model.Library{Name: "L", RootPath: t.TempDir(), Enabled: true, IsDefault: true, UploadPolicy: model.PolicyLocal})
	if err != nil {
		t.Fatal(err)
	}
	vid, _, err := st.UpsertVideo(&model.Video{Platform: "pornhub", PlatformVideoID: "v1", Title: "T", URL: "u"})
	if err != nil {
		t.Fatal(err)
	}

	f := &model.LibraryFile{VideoID: vid, LibraryID: libID, RelDir: "Pornhub/A/T", VideoName: "T",
		VideoPath: "/tmp/T.mp4", NFOPath: "/tmp/T.nfo", SizeBytes: 1234, Width: 1920, Height: 1080, DurationSec: 30}
	if err := st.SaveLibraryFile(f); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	// 幂等覆盖
	f.SizeBytes = 2048
	if err := st.SaveLibraryFile(f); err != nil {
		t.Fatalf("覆盖失败: %v", err)
	}
	got, err := st.GetLibraryFile(vid, libID)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got.SizeBytes != 2048 || got.Height != 1080 {
		t.Fatalf("覆盖未生效: %+v", got)
	}

	has, err := st.HasLocalFile(vid)
	if err != nil || !has {
		t.Fatal("应判定为已有本地文件")
	}
	m, err := st.LocalFileMap([]int64{vid})
	if err != nil || len(m[vid]) != 1 || m[vid][0] != libID {
		t.Fatalf("LocalFileMap 错误: %+v", m)
	}
	usage, err := st.LibraryUsages()
	if err != nil {
		t.Fatal(err)
	}
	if u := usage[libID]; u.FileCount != 1 || u.SizeBytes != 2048 {
		t.Fatalf("统计错误: %+v", u)
	}

	// 列表页应带上本地副本标记
	items, _, err := st.ListFeed(FeedQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || len(items[0].LocalLibraryIDs) != 1 {
		t.Fatalf("列表未标记本地副本: %+v", items)
	}

	if err := st.DeleteLibraryFile(vid, libID); err != nil {
		t.Fatal(err)
	}
	if has, _ := st.HasLocalFile(vid); has {
		t.Fatal("删除索引后不应再有本地文件标记")
	}
}

func TestJobControls(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()

	id, err := st.EnqueueTarget(model.JobTypeDownload, 42, map[string]any{"videoId": 42}, 10)
	if err != nil {
		t.Fatal(err)
	}
	// 排队中直接暂停
	status, err := st.RequestPause(id)
	if err != nil || status != model.JobPaused {
		t.Fatalf("排队任务应直接变 paused，得到 %q (%v)", status, err)
	}
	// 列表接口要能读回暂停状态（cancel_requested 存的是 0/1/2，曾因扫进 bool 而报错）
	if jobs, jerr := st.ListJobs(model.JobTypeDownload, "", 10); jerr != nil {
		t.Fatalf("列出任务失败: %v", jerr)
	} else if len(jobs) != 1 || jobs[0].Status != model.JobPaused {
		t.Fatalf("暂停状态未正确回读: %+v", jobs)
	}
	// 恢复 → 重新排队
	if ok, err := st.RetryJob(id); err != nil || !ok {
		t.Fatalf("恢复失败: %v", err)
	}
	// 领取后取消 → 打标记
	job, err := st.Claim(model.JobTypeDownload)
	if err != nil || job == nil {
		t.Fatalf("领取失败: %v", err)
	}
	// 运行中暂停：打 2 号标记，列表接口必须能读回（老代码在这里 500）
	if st2, perr := st.RequestPause(job.ID); perr != nil || st2 != model.JobRunning {
		t.Fatalf("运行中暂停应返回 running，得到 %q (%v)", st2, perr)
	}
	if jobs, jerr := st.ListJobs(model.JobTypeDownload, "", 10); jerr != nil {
		t.Fatalf("暂停后列出任务失败: %v", jerr)
	} else if jobs[0].CancelRequested != 2 {
		t.Fatalf("暂停标记应回读为 2，得到 %d", jobs[0].CancelRequested)
	}
	if _, cerr := st.CancelFlag(job.ID); cerr != nil {
		t.Fatalf("读取控制位失败: %v", cerr)
	}

	status, err = st.RequestCancel(job.ID)
	if err != nil || status != model.JobRunning {
		t.Fatalf("运行中取消应返回 running，得到 %q (%v)", status, err)
	}
	flag, _ := st.CancelFlag(job.ID)
	if flag != 1 {
		t.Fatalf("取消标记应为 1，得到 %d", flag)
	}
	_ = st.FinishJob(job.ID, model.JobFailed, "boom")
	if ok, _ := st.RetryJob(job.ID); !ok {
		t.Fatal("失败任务应可重试")
	}
	if ok, _ := st.HasActiveJobForTarget(model.JobTypeDownload, 42); !ok {
		t.Fatal("重新排队后应判定为活跃任务")
	}
	if err := st.DeleteJob(job.ID); err != nil {
		t.Fatal(err)
	}
	// 活跃任务不允许删除
	if err := st.DeleteJob(job.ID); err != nil {
		t.Fatal(err) // 已删除，第二次执行也没错
	}
}

// 「关注」流不该再出现已经下到本地库的片（2026-11-03 用户要求）。
// 判据必须跟着 library_files 走：媒体库里把文件删掉之后，这条片要重新回到流里，
// 所以这里连「删了就回来」一起测。
func TestListFeedHideDownloaded(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()

	libID, err := st.CreateLibrary(&model.Library{Name: "L", RootPath: t.TempDir(), Enabled: true, IsDefault: true, UploadPolicy: model.PolicyLocal})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	ids := make([]int64, 3)
	for i := 0; i < 3; i++ {
		pub := base.Add(time.Duration(i) * time.Hour)
		ids[i], _, err = st.UpsertVideo(&model.Video{
			Platform: "pornhub", PlatformVideoID: "h" + strconv.Itoa(i),
			Title: "片" + strconv.Itoa(i), URL: "u", PublishedAt: &pub,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// 中间那条已经下到本地
	if err := st.SaveLibraryFile(&model.LibraryFile{
		VideoID: ids[1], LibraryID: libID, RelDir: "x", VideoName: "片1.mp4",
		VideoPath: "/tmp/片1.mp4", SizeBytes: 1,
	}); err != nil {
		t.Fatal(err)
	}

	withLocal, _, err := st.ListFeed(FeedQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(withLocal) != 3 {
		t.Fatalf("不过滤时应仍能列出 3 条，得到 %d", len(withLocal))
	}

	hidden, _, err := st.ListFeed(FeedQuery{Limit: 10, HideDownloaded: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(hidden) != 2 {
		t.Fatalf("应只剩 2 条，得到 %d", len(hidden))
	}
	for _, v := range hidden {
		if v.ID == ids[1] {
			t.Fatal("已下载的那条不该出现在关注流里")
		}
	}
	// 用「发布时间倒序 + id 倒序」的游标翻页时，被滤掉的那条不能把分页卡住
	page, next, err := st.ListFeed(FeedQuery{Limit: 1, HideDownloaded: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || next == "" {
		t.Fatalf("第一页应有 1 条并给游标：%d / %q", len(page), next)
	}
	rest, _, err := st.ListFeed(FeedQuery{Limit: 10, Cursor: next, HideDownloaded: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || rest[0].ID == ids[1] {
		t.Fatalf("第二页应有 1 条且不是被滤掉那条：%+v", rest)
	}

	// 媒体库里删掉文件 → 这条片重新回到关注流
	if err := st.DeleteLibraryFile(ids[1], libID); err != nil {
		t.Fatal(err)
	}
	back, _, err := st.ListFeed(FeedQuery{Limit: 10, HideDownloaded: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 3 {
		t.Fatalf("删掉本地文件后应回到 3 条，得到 %d", len(back))
	}
}
