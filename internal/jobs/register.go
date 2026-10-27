package jobs

import (
	"strconv"
	"strings"

	"github.com/mario/myporn/internal/model"
)

// RegisterDefaults 注册所有内置任务类型。并发上限每次领取时动态读取设置，
// 因此用户在设置页改并发数立即生效（无需重启）。
func (r *Runner) RegisterDefaults() {
	r.Register(model.JobTypeSync, func() int {
		return r.settingInt("fetch_concurrency", r.cfg.MaxFetchJobs)
	}, r.runSyncJob)

	r.Register(model.JobTypeDownload, func() int {
		return r.settingInt("download_concurrency", r.cfg.MaxDownload)
	}, r.runDownloadJob)

	// 社交动态：一个平台同时只跑一个（限流是按 IP 的，并发只会更快撞墙），
	// 所以并发上限固定 1，不分平台开关。
	if r.social != nil {
		r.Register(model.JobTypeSocialSync, func() int { return 1 }, r.runSocialSyncJob)
		r.Register(model.JobTypeSocialSave, func() int {
			return r.settingInt("download_concurrency", r.cfg.MaxDownload)
		}, r.runSocialSaveJob)
	}
}

func (r *Runner) settingInt(key string, def int) int {
	v, err := r.st.GetSetting(key)
	if err != nil {
		return def
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	if n == 0 {
		return def
	}
	if n > 16 {
		return 16
	}
	return n
}
