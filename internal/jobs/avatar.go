package jobs

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mario/myporn/internal/model"
	"github.com/mario/myporn/internal/platform"
	"github.com/mario/myporn/internal/store"
)

const (
	// avatarRetryAfter 是「这家没这个艺人」的冷却时间：艺人列表页会反复触发补齐，
	// 别每刷一次页面就把所有头像源问一遍。
	avatarRetryAfter = 30 * time.Minute
	// avatarLookupTimeout 限制单次补头像的总耗时，不能拖慢同步或页面接口。
	avatarLookupTimeout = 15 * time.Second
)

// AvatarFiller 给本地没有头像的艺人从站外头像源借一张。
// 只在本地头像为空时才动手，命中就写回数据库，之后不再重复查。
type AvatarFiller struct {
	st      *store.Store
	sources *platform.AvatarRegistry
	log     *slog.Logger

	mu      sync.Mutex
	cooling map[int64]time.Time // 艺人 id -> 上次没查到的时间
	running map[int64]bool      // 正在查的艺人，避免并发重复请求
}

// NewAvatarFiller 构造头像补齐器；sources 为空时它什么都不做。
func NewAvatarFiller(st *store.Store, sources *platform.AvatarRegistry, log *slog.Logger) *AvatarFiller {
	return &AvatarFiller{st: st, sources: sources, log: log,
		cooling: map[int64]time.Time{}, running: map[int64]bool{}}
}

// Lookup 按艺人名依次问各头像源，返回第一个命中的地址（不落库，预览用）。
func (f *AvatarFiller) Lookup(ctx context.Context, name string) string {
	if f == nil || strings.TrimSpace(name) == "" {
		return ""
	}
	sources := f.sources.All()
	if len(sources) == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, avatarLookupTimeout)
	defer cancel()
	for _, src := range sources {
		url, err := src.ResolveAvatar(ctx, name)
		if err != nil {
			f.log.Debug("头像源未命中", "source", src.Key(), "name", name, "err", err)
			continue
		}
		if strings.TrimSpace(url) == "" {
			continue
		}
		return url
	}
	return ""
}

// Fill 给已入库的艺人补头像；命中则写回数据库并返回地址，没命中返回空串。
func (f *AvatarFiller) Fill(ctx context.Context, p *model.Performer) string {
	if f == nil || p == nil || p.ID == 0 {
		return ""
	}
	if strings.TrimSpace(p.AvatarURL) != "" {
		return p.AvatarURL
	}
	if !f.begin(p.ID) {
		return ""
	}
	url := f.Lookup(ctx, p.Name)
	if url == "" {
		f.end(p.ID, false)
		return ""
	}
	if err := f.st.SetPerformerAvatar(p.ID, url); err != nil {
		f.log.Warn("写入艺人头像失败", "performer", p.ID, "err", err)
		f.end(p.ID, false)
		return ""
	}
	f.end(p.ID, true)
	f.log.Info("已补齐艺人头像", "performer", p.ID, "name", p.Name, "url", url)
	return url
}

// FillMissing 一次性补齐所有没有头像的艺人，返回检查数与写入数。
func (f *AvatarFiller) FillMissing(ctx context.Context) (checked, filled int, err error) {
	if f == nil {
		return 0, 0, nil
	}
	items, err := f.st.ListPerformersWithoutAvatar()
	if err != nil {
		return 0, 0, err
	}
	for _, p := range items {
		if ctx.Err() != nil {
			return checked, filled, ctx.Err()
		}
		checked++
		if url := f.Fill(ctx, p); url != "" {
			filled++
		} else {
			f.log.Warn("没找到头像", "id", p.ID, "name", p.Name)
		}
	}
	return checked, filled, nil
}

// begin 判断这次补齐是否值得发起（在冷却期内或已有并发请求时跳过）。
func (f *AvatarFiller) begin(id int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running[id] {
		return false
	}
	if at, ok := f.cooling[id]; ok && time.Since(at) < avatarRetryAfter {
		return false
	}
	f.running[id] = true
	return true
}

// end 收尾：成功就不再冷却，失败则记下时间等一会儿再试。
func (f *AvatarFiller) end(id int64, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.running, id)
	if ok {
		delete(f.cooling, id)
		return
	}
	f.cooling[id] = time.Now()
}
