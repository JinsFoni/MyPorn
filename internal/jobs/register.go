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
