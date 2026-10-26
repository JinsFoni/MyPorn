// Package beeg 接入 beeg.com。当前只做艺人头像：
// beeg 的公开数据接口（store.externulls.com）能按名字查到艺人，
// 返回的 tag 里带着站内自用的那张头像照片，比从 Pornhub 页面 DOM 里抠稳定得多。
//
// 头像地址形如
//
//	https://thumbs.externulls.com/photos/{photoId}/to.webp?crop_id={cropId}
//
// 不带尺寸参数时就是裁切后的原图（本机实测 530x529 起），
// 前端要多大由图片代理补 size_new 参数，见 httpapi.handleImage。
package beeg

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/mario/myporn/internal/platform"
)

const (
	// storeAPI 是 beeg 前端 SPA 使用的公开数据接口。
	storeAPI = "https://store.externulls.com"
	// thumbBase 是图片（照片/视频预览）域名。
	thumbBase = "https://thumbs.externulls.com"

	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// AvatarProvider 用 beeg 的 tag 接口按艺人名取头像。
type AvatarProvider struct {
	client    *http.Client
	storeAPI  string // 可替换，便于测试
	thumbBase string
}

// NewAvatarProvider 用给定的 HTTP 客户端（可带代理）构造头像来源。
func NewAvatarProvider(client *http.Client) *AvatarProvider {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &AvatarProvider{client: client, storeAPI: storeAPI, thumbBase: thumbBase}
}

// Key 实现 platform.AvatarProvider。
func (a *AvatarProvider) Key() string { return "beeg" }

// tag 是 /tag 接口返回的艺人（beeg 内部把人和厂牌统称 tag），只取需要的字段。
type tag struct {
	Name   string `json:"tg_name"`
	Slug   string `json:"tg_slug"`
	Thumbs []struct {
		ID    int64 `json:"id"`
		Crops []struct {
			ID int64 `json:"id"`
		} `json:"crops"`
	} `json:"thumbs"`
}

// ResolveAvatar 按艺人名查 beeg 头像。名字对不上就报错，绝不"猜一个"。
func (a *AvatarProvider) ResolveAvatar(ctx context.Context, performerName string) (string, error) {
	slug := slugify(performerName)
	if slug == "" {
		return "", fmt.Errorf("艺人名无法转成 beeg slug: %q", performerName)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.storeAPI+"/tag?slug="+url.QueryEscape(slug), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 beeg 头像接口失败: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", fmt.Errorf("beeg 上没有艺人 %q", performerName)
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("beeg 头像接口返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("读取 beeg 头像接口响应失败: %w", err)
	}
	var t tag
	if err := json.Unmarshal(body, &t); err != nil {
		return "", fmt.Errorf("解析 beeg 头像接口响应失败: %w", err)
	}
	// 接口是按 slug 模糊查的，必须确认查回来的就是这个艺人。
	if slugify(t.Name) != slug {
		return "", fmt.Errorf("beeg 返回的艺人 %q 与 %q 不匹配", t.Name, performerName)
	}
	if len(t.Thumbs) == 0 || t.Thumbs[0].ID == 0 {
		return "", fmt.Errorf("beeg 上 %q 没有头像照片", performerName)
	}
	thumb := t.Thumbs[0]
	src := fmt.Sprintf("%s/photos/%d/to.webp", a.thumbBase, thumb.ID)
	if len(thumb.Crops) > 0 && thumb.Crops[0].ID != 0 {
		src += fmt.Sprintf("?crop_id=%d", thumb.Crops[0].ID)
	}
	return src, nil
}

// slugify 把艺人名压成 beeg 的 slug（折叠变音符号、只留字母数字、转小写）。
// 规则和 X 账号名的猜测共用，实现在 platform.Slugify。
func slugify(name string) string { return platform.Slugify(name) }

// 编译期检查：AvatarProvider 满足 platform.AvatarProvider。
var _ platform.AvatarProvider = (*AvatarProvider)(nil)
