package bilibili

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// 本文件：扫码登录（passport 的 qrcode 两接口）+ 一条可续期凭据的表示。
//
// 为什么要扫码这条路（2026-10-30 实测）：手填 Cookie 这条路有两个躲不开的坑——
//
//  1. SESSDATA 是 HttpOnly，控制台 document.cookie 里根本没有它（实测用户照此粘来的
//     16 字段串打 nav 一律 -101）；
//  2. 手填的串里没有 refresh_token（B 站叫 ac_time_value），于是登录态一旦被 B 站作废
//     就只能重填——实测一条 SESSDATA 内嵌到期 2027-04-25 的登录态，第 5 天就 -101。
//
// 扫码一次同时解决两件事：SESSDATA / bili_jct / DedeUserID 直接从 Set-Cookie 落库
// （不经过 JS），refresh_token 从响应 JSON 拿，后续才能自动续期（见 refresh.go）。
// 这套流程参照 B 站第三方文档（bilibili-API-collect passport 登录）与 bili-sync 的实现。
const (
	// qrcodeGenerateURL 生成登录二维码：返回 data.url（转成二维码图片给前端）和 data.qrcode_key。
	qrcodeGenerateURL = "https://passport.bilibili.com/x/passport-login/web/qrcode/generate"
	// qrcodePollURL 轮询扫码结果。
	qrcodePollURL = "https://passport.bilibili.com/x/passport-login/web/qrcode/poll"

	// buvidURL 取一个 buvid3（浏览器指纹）。**不需要登录**，扫码成功后单独取一条填进凭据：
	// 少了它 B 站会把请求当「无指纹客户端」，风控更容易命中。
	buvidURL = "https://api.bilibili.com/x/web-frontend/getbuvid"
)

// 扫码轮询的状态码（B 站原文）。
const (
	qrCodeSuccess          = 0
	qrCodeNotScanned       = 86101
	qrCodeScannedNoConfirm = 86090
	qrCodeExpired          = 86038
)

// Credential 是一条「能自己续期」的 B 站登录态。
//
// 与手填 Cookie 的区别只有第五条：RefreshToken（响应里的 refresh_token，浏览器那边存成
// ac_time_value cookie）。带它才能走 cookie/refresh，不带就只能等 B 站作废。
type Credential struct {
	Sessdata     string `json:"sessdata"`
	BiliJct      string `json:"bili_jct"`
	Buvid3       string `json:"buvid3"`
	DedeUserID   string `json:"dedeuserid"`
	RefreshToken string `json:"refresh_token"`
}

// Complete 报告五个字段是不是都齐了（缺任一都没法既登录又续期）。
func (c Credential) Complete() bool {
	return c.Sessdata != "" && c.BiliJct != "" && c.Buvid3 != "" && c.DedeUserID != "" && c.RefreshToken != ""
}

// CookieHeader 拼出实际发出去的 Cookie 头。
//
// 只发这四个：bili-sync 同款做法，实测够用（多发的 b_nut / buvid4 之类只是浏览器自己的东西）。
// 顺序固定，便于日志里肉眼比对哪一段换了。
func (c Credential) CookieHeader() string {
	parts := make([]string, 0, 4)
	if c.Buvid3 != "" {
		parts = append(parts, "buvid3="+c.Buvid3)
	}
	if c.Sessdata != "" {
		parts = append(parts, "SESSDATA="+c.Sessdata)
	}
	if c.BiliJct != "" {
		parts = append(parts, "bili_jct="+c.BiliJct)
	}
	if c.DedeUserID != "" {
		parts = append(parts, "DedeUserID="+c.DedeUserID)
	}
	return strings.Join(parts, "; ")
}

// QRCode 是一次扫码会话里前端要的两样东西。
type QRCode struct {
	URL string `json:"url"`
	Key string `json:"key"`
}

// QRStatus 是轮询结果的状态，前端按它切界面。
type QRStatus string

const (
	QRStatusPending QRStatus = "pending" // 还没扫
	QRStatusScanned QRStatus = "scanned" // 扫了没确认
	QRStatusSuccess QRStatus = "success" // 成功（Credential 有值）
	QRStatusExpired QRStatus = "expired" // 过期，要重新生成
)

// QRResult 是轮询一次的结论。
type QRResult struct {
	Status     QRStatus
	Message    string
	Credential *Credential
}

// GenerateQRCode 向 passport 要一张登录二维码。
func (a *Adapter) GenerateQRCode(ctx context.Context) (QRCode, error) {
	body, _, err := a.authCall(ctx, http.MethodGet, qrcodeGenerateURL, "", nil)
	if err != nil {
		return QRCode{}, fmt.Errorf("生成登录二维码失败: %w", err)
	}
	var r struct {
		Code int    `json:"code"`
		Msg  string `json:"message"`
		Data struct {
			URL string `json:"url"`
			Key string `json:"qrcode_key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return QRCode{}, fmt.Errorf("二维码接口返回的不是 JSON: %w", err)
	}
	if r.Code != 0 || r.Data.URL == "" || r.Data.Key == "" {
		return QRCode{}, fmt.Errorf("二维码接口返回异常（code=%d %s）", r.Code, r.Msg)
	}
	return QRCode{URL: r.Data.URL, Key: r.Data.Key}, nil
}

// PollQRCode 问一次「扫了没有」，成功时顺手把凭据拼出来（含新取的 buvid3）。
func (a *Adapter) PollQRCode(ctx context.Context, key string) (QRResult, error) {
	if strings.TrimSpace(key) == "" {
		return QRResult{}, fmt.Errorf("缺少 qrcode_key")
	}
	u := qrcodePollURL + "?qrcode_key=" + url.QueryEscape(key)
	body, resp, err := a.authCall(ctx, http.MethodGet, u, "", nil)
	if err != nil {
		return QRResult{}, err
	}
	var r struct {
		Code int `json:"code"`
		Data struct {
			Code         int    `json:"code"`
			Message      string `json:"message"`
			RefreshToken string `json:"refresh_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return QRResult{}, fmt.Errorf("轮询接口返回的不是 JSON: %w", err)
	}
	if r.Code != 0 {
		return QRResult{}, fmt.Errorf("轮询接口返回异常（code=%d）", r.Code)
	}
	switch r.Data.Code {
	case qrCodeSuccess:
		cred, err := credentialFromResponse(resp, r.Data.RefreshToken)
		if err != nil {
			return QRResult{}, err
		}
		// buvid3 单独取：这一步失败不该让整次登录白费（续期不依赖它）。
		if buvid, err := a.fetchBuvid3(ctx); err == nil {
			cred.Buvid3 = buvid
		}
		return QRResult{Status: QRStatusSuccess, Message: "登录成功", Credential: &cred}, nil
	case qrCodeNotScanned:
		return QRResult{Status: QRStatusPending, Message: "等待扫码"}, nil
	case qrCodeScannedNoConfirm:
		return QRResult{Status: QRStatusScanned, Message: "已扫描，请在手机上确认"}, nil
	case qrCodeExpired:
		return QRResult{Status: QRStatusExpired, Message: "二维码已过期，请刷新重新生成"}, nil
	default:
		return QRResult{}, fmt.Errorf("未知的扫码状态 code=%d %s", r.Data.Code, r.Data.Message)
	}
}

// fetchBuvid3 取一条浏览器指纹。
func (a *Adapter) fetchBuvid3(ctx context.Context) (string, error) {
	body, _, err := a.authCall(ctx, http.MethodGet, buvidURL, "", nil)
	if err != nil {
		return "", err
	}
	var r struct {
		Code int `json:"code"`
		Data struct {
			Buvid string `json:"buvid"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", err
	}
	if r.Data.Buvid == "" {
		return "", fmt.Errorf("buvid 接口没给 buvid（code=%d）", r.Code)
	}
	return r.Data.Buvid, nil
}

// credentialFromResponse 从登录响应的 Set-Cookie 里挑三件套，配上 JSON 里的 refresh_token。
//
// 为什么从 Set-Cookie 拿：SESSDATA 是 HttpOnly，这是唯一能拿到它的地方——也让整条
// 「让用户在控制台里复制」的路径彻底消失（2026-10-30 踩过：那样拿到的串里没有它）。
func credentialFromResponse(resp *http.Response, refreshToken string) (Credential, error) {
	if resp == nil {
		return Credential{}, fmt.Errorf("登录响应为空")
	}
	var cred Credential
	cred.RefreshToken = strings.TrimSpace(refreshToken)
	for _, c := range resp.Cookies() {
		switch c.Name {
		case "SESSDATA":
			cred.Sessdata = c.Value
		case "bili_jct":
			cred.BiliJct = c.Value
		case "DedeUserID":
			cred.DedeUserID = c.Value
		case "buvid3":
			cred.Buvid3 = c.Value
		}
	}
	switch {
	case cred.Sessdata == "":
		return Credential{}, fmt.Errorf("登录响应里没有 SESSDATA（B 站可能改了登录流程）")
	case cred.BiliJct == "" || cred.DedeUserID == "":
		return Credential{}, fmt.Errorf("登录响应里缺 bili_jct 或 DedeUserID")
	case cred.RefreshToken == "":
		// 没有 refresh_token 也能用，但就退化成手填 Cookie 那种「作废后只能重填」。
		return Credential{}, fmt.Errorf("登录响应里没有 refresh_token，无法自动续期")
	}
	return cred, nil
}

// authCall 是 passport / 指纹接口用的底层请求：GET 或 form POST，返回正文和响应。
//
// 不复用 rawGet 的原因：这里要看 Set-Cookie（那是半个登录结果），而且 passport 的
// 报错是「HTTP 200 + JSON code≠0」，不能按状态码判失败。
func (a *Adapter) authCall(ctx context.Context, method, u, cookie string, form url.Values) ([]byte, *http.Response, error) {
	var bodyReader io.Reader
	if form != nil {
		bodyReader = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Referer", a.base+"/")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, resp, fmt.Errorf("上游返回 %d（%s）", resp.StatusCode, u)
	}
	return body, resp, nil
}
