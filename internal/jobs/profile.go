package jobs

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/mario/myporn/internal/model"
	"github.com/mario/myporn/internal/platform"
	"github.com/mario/myporn/internal/store"
)

// profileLookupTimeout 限制单次找资料的总耗时：两个候选标识 + 一次搜索，
// 每个请求之间还有站点的礼貌间隔，卡住时不能把同步拖死。
const profileLookupTimeout = 25 * time.Second

// ProfileFiller 把站外（Pornhub）的完整档案借给资料很薄的平台。
//
// beeg / xHamster 的艺人页只有名字、头像和两个站内统计，简介、资料标签、
// 三围、社交链接、排名全缺；同一个人在 Pornhub 上往往有完整档案。
// 借的时候只换「资料内容」，平台标识、主页链接、影片数、订阅数留在原平台，
// 具体口径见 model.Performer.AdoptProfileFrom。
type ProfileFiller struct {
	st      *store.Store
	sources *platform.ProfileRegistry
	log     *slog.Logger
}

// NewProfileFiller 构造资料补齐器；sources 为空时它什么都不做。
func NewProfileFiller(st *store.Store, sources *platform.ProfileRegistry, log *slog.Logger) *ProfileFiller {
	return &ProfileFiller{st: st, sources: sources, log: log}
}

// Resolve 按艺人名到资料源找一份资料（不落库；订阅预览用）。
// platform 是艺人所属平台：和资料源同源的跳过（Pornhub 的艺人不用再去 Pornhub 找）。
func (f *ProfileFiller) Resolve(ctx context.Context, name, platform string) *model.Performer {
	if f == nil || strings.TrimSpace(name) == "" {
		return nil
	}
	sources := f.sources.All()
	if len(sources) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, profileLookupTimeout)
	defer cancel()
	for _, src := range sources {
		if src.Key() == platform {
			continue
		}
		p, err := src.ResolveProfile(ctx, name)
		if err != nil {
			f.log.Debug("资料源未命中", "source", src.Key(), "name", name, "err", err)
			continue
		}
		if p == nil || strings.TrimSpace(p.Name) == "" {
			continue
		}
		return p
	}
	return nil
}

// Fill 给已入库的艺人换一份资料源资料；真的写入了才返回 true。
// p 会被就地改动（调用方手里那份也会跟着更新）。
func (f *ProfileFiller) Fill(ctx context.Context, p *model.Performer) bool {
	if f == nil || p == nil || p.ID == 0 {
		return false
	}
	src := f.Resolve(ctx, p.Name, p.Platform)
	if src == nil {
		return false
	}
	if !p.AdoptProfileFrom(src) {
		return false // 已经是同一份资料，不必再写库
	}
	if err := f.st.UpdatePerformerProfile(p); err != nil {
		f.log.Warn("写入艺人资料失败", "performer", p.ID, "err", err)
		return false
	}
	f.log.Info("已借用站外艺人资料", "performer", p.ID, "name", p.Name, "platform", p.Platform)
	return true
}
