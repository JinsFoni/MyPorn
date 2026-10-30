package bilibili

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

// 本文件：凭据续期。B 站的登录态不是「到期就走」，而是「临近过期时给你一次换新的机会」——
// 只要还在用，就能一直续下去；一旦错过（或被人为作废），只能重新扫码。
//
// 四步（顺序不能换，每一步的 token 都来自上一步）：
//
//  1. GET  passport /x/passport-login/web/cookie/info  → data.refresh（要不要续）
//  2. GET  www /correspond/1/<RSA 加密的时间戳>         → 页面里抠 refresh_csrf
//  3. POST passport /x/passport-login/web/cookie/refresh → Set-Cookie 里是新的三件套，
//     JSON 里是新的 refresh_token
//  4. POST passport /x/passport-login/web/confirm/refresh → 拿**新** bili_jct + **旧**
//     refresh_token 确认，旧的登录态到此作废
//
// 第 2 步的 RSA 公钥是 B 站前端写死的（对应 bilibili-API-collect 的 cookie 刷新文档），
// 用 RSA-OAEP(SHA-256) 加密 `refresh_<毫秒时间戳>`。时间戳往前挪 20 秒是防本机时钟比
// 服务器快——服务端只认它自己时间窗内的值。
const (
	cookieInfoURL    = "https://passport.bilibili.com/x/passport-login/web/cookie/info"
	cookieRefreshURL = "https://passport.bilibili.com/x/passport-login/web/cookie/refresh"
	cookieConfirmURL = "https://passport.bilibili.com/x/passport-login/web/confirm/refresh"

	// correspondPath 是第 2 步的入口，域名是 www（不是 passport）。
	correspondPath = "/correspond/1/"
)

// refreshPublicKeyPEM 是 B 站前端里那把固定的公钥（只用来加密一个时间戳，不是机密）。
const refreshPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDLgd2OAkcGVtoE3ThUREbio0Eg
Uc/prcajMKXvkCKFCWhJYJcLkcM2DKKcSeFpD/j6Boy538YXnR6VhcuUJOhH2x71
nzPjfdTcqMz7djHum0qSZA0AyCBDABUqCrfNgCiJ00Ra7GmRj+YCK1NJEuewlb40
JNrRuoEUXpabUzGB8QIDAQAB
-----END PUBLIC KEY-----`

// refreshCSRFPattern 从 correspond 页面里抠 refresh_csrf。
var refreshCSRFPattern = regexp.MustCompile(`<div id="1-name">(.+?)</div>`)

// NeedRefresh 问 B 站「这条登录态该续了吗」。
//
// 返回值语义：true = 该续；false = 不用续（也包含「还没到时间」）；
// error 只表示「问不出来」（网络/风控），这时**不要**当成「失效」。
func (a *Adapter) NeedRefresh(ctx context.Context, cred Credential) (bool, error) {
	body, _, err := a.authCall(ctx, http.MethodGet, cookieInfoURL, cred.CookieHeader(), nil)
	if err != nil {
		return false, err
	}
	var r struct {
		Code int `json:"code"`
		Msg  string
		Data struct {
			Refresh bool `json:"refresh"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return false, fmt.Errorf("cookie/info 返回的不是 JSON: %w", err)
	}
	if r.Code == -101 {
		// 2026-10-30 实测：一条已作废的登录态打这里回 {"code":-101,"message":"账号未登录"}。
		// 这不是「不该续」，而是「没得续」——只能重新扫码。
		return false, fmt.Errorf("登录态已失效（cookie/info 回 -101 账号未登录），只能重新扫码登录")
	}
	if r.Code != 0 {
		return false, fmt.Errorf("cookie/info 返回 code=%d", r.Code)
	}
	return r.Data.Refresh, nil
}

// RefreshCredential 走完四步，返回**新**的凭据（旧的 SESSDATA / refresh_token 到第 4 步后作废）。
func (a *Adapter) RefreshCredential(ctx context.Context, cred Credential) (Credential, error) {
	if cred.RefreshToken == "" {
		return Credential{}, fmt.Errorf("没有 refresh_token（手填的 Cookie 一般都没有），只能重新扫码登录")
	}
	csrf, err := a.fetchRefreshCSRF(ctx, cred)
	if err != nil {
		return Credential{}, fmt.Errorf("获取 refresh_csrf 失败: %w", err)
	}
	form := url.Values{
		"csrf":          {cred.BiliJct},
		"refresh_csrf":  {csrf},
		"refresh_token": {cred.RefreshToken},
		"source":        {"main_web"},
	}
	body, resp, err := a.authCall(ctx, http.MethodPost, cookieRefreshURL, cred.CookieHeader(), form)
	if err != nil {
		return Credential{}, fmt.Errorf("刷新登录态失败: %w", err)
	}
	var r struct {
		Code int `json:"code"`
		Msg  string
		Data struct {
			RefreshToken string `json:"refresh_token"`
			Message      string `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Credential{}, fmt.Errorf("刷新接口返回的不是 JSON: %w", err)
	}
	if r.Code != 0 {
		return Credential{}, fmt.Errorf("刷新接口返回 code=%d %s", r.Code, r.Data.Message)
	}
	fresh, err := credentialFromResponse(resp, r.Data.RefreshToken)
	if err != nil {
		return Credential{}, err
	}
	// buvid3 不会随刷新变，沿用旧的（新响应里通常也没有它）。
	fresh.Buvid3 = cred.Buvid3
	// 第 4 步：确认。到这里新凭据才真正生效，旧凭据作废；用**旧** refresh_token 确认。
	confirm := url.Values{
		"csrf":          {fresh.BiliJct},
		"refresh_token": {cred.RefreshToken},
	}
	if _, _, err := a.authCall(ctx, http.MethodPost, cookieConfirmURL, fresh.CookieHeader(), confirm); err != nil {
		return Credential{}, fmt.Errorf("确认刷新失败: %w", err)
	}
	return fresh, nil
}

// fetchRefreshCSRF 完成第 2 步：RSA 加密时间戳 → 打开 correspond 页面 → 抠 refresh_csrf。
func (a *Adapter) fetchRefreshCSRF(ctx context.Context, cred Credential) (string, error) {
	enc, err := encryptCorrespondTime(time.Now())
	if err != nil {
		return "", err
	}
	body, _, err := a.authCall(ctx, http.MethodGet, a.base+correspondPath+enc, cred.CookieHeader(), nil)
	if err != nil {
		return "", err
	}
	m := refreshCSRFPattern.FindSubmatch(body)
	if len(m) < 2 {
		// 页面结构变了 / 被风控换成了验证页：两种都会抠不到。
		return "", fmt.Errorf("correspond 页面里没找到 refresh_csrf（页面可能被风控替换）")
	}
	return string(m[1]), nil
}

// encryptCorrespondTime 把 `refresh_<毫秒时间戳>` 用 B 站公钥做 RSA-OAEP(SHA-256) 加密并转 hex。
//
// 时间戳往前挪 20 秒：本机时钟可能比服务器快，服务端只认自己时间窗内的值。
func encryptCorrespondTime(now time.Time) (string, error) {
	block, _ := pem.Decode([]byte(refreshPublicKeyPEM))
	if block == nil {
		return "", fmt.Errorf("内置公钥解析失败")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("内置公钥不是 PKIX 格式: %w", err)
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return "", fmt.Errorf("内置公钥不是 RSA")
	}
	plain := []byte("refresh_" + strconv.FormatInt(now.UnixMilli()-20000, 10))
	cipher, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, rsaPub, plain, nil)
	if err != nil {
		return "", fmt.Errorf("加密时间戳失败: %w", err)
	}
	return hex.EncodeToString(cipher), nil
}
