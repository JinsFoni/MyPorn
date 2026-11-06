// Package config 负责从环境变量与 .env.local 加载运行配置。
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type OpenList struct {
	BaseURL string
	Token   string
	Root    string
}

type Config struct {
	Version       string
	Port          string
	Password      string
	SecretKey     string // base64 32 bytes, 用于 AES-GCM
	DataDir       string
	DBPath        string
	LibraryRoot   string
	GlobalProxy   string
	PlatformProxy map[string]string
	OpenList      OpenList
	MaxFetchJobs  int
	MaxDownload   int
	MaxUpload     int
	SyncHours     int
	// ImgCacheMaxMB 是图片磁盘缓存上限（MB），0 表示不限。
	ImgCacheMaxMB int
	FFmpegPath    string
	FFprobePath   string
	// YtDlpPath 是 yt-dlp 可执行文件（YouTube 深度回填与下载用）；空表示按 PATH 找。
	YtDlpPath string
	// DownloadRateKB 是单任务下载限速（KB/s），0 表示不限。
	DownloadRateKB int
}

// Load 读取 .env.local（不存在则忽略）后再从环境变量组装配置。
func Load(root string) (*Config, error) {
	_ = LoadDotEnv(filepath.Join(root, ".env.local"))
	_ = LoadDotEnv(filepath.Join(root, ".env"))

	dataDir := env("DATA_DIR", "./data")
	c := &Config{
		Version:     "0.1.1",
		Port:        env("PORT", "8080"),
		Password:    env("APP_PASSWORD", "changeme"),
		SecretKey:   env("APP_ENCRYPTION_KEY", ""),
		DataDir:     dataDir,
		DBPath:      env("DATABASE_PATH", filepath.Join(dataDir, "myporn.db")),
		LibraryRoot: env("LIBRARY_ROOT", "./media"),
		GlobalProxy: env("GLOBAL_PROXY", ""),
		PlatformProxy: map[string]string{
			"pornhub":   env("PLATFORM_PROXY_PORNHUB", ""),
			"xhamster":  env("PLATFORM_PROXY_XHAMSTER", ""),
			"beeg":      env("PLATFORM_PROXY_BEEG", ""),
			"x":         env("PLATFORM_PROXY_X", ""),
			"instagram": env("PLATFORM_PROXY_INSTAGRAM", ""),
			"youtube":   env("PLATFORM_PROXY_YOUTUBE", ""),
		},
		OpenList: OpenList{
			BaseURL: env("OPENLIST_BASE_URL", ""),
			Token:   env("OPENLIST_TOKEN", ""),
			Root:    env("OPENLIST_ROOT", ""),
		},
		MaxFetchJobs:   envInt("MAX_FETCH_JOBS", 4),
		MaxDownload:    envInt("MAX_DOWNLOAD_JOBS", 2),
		MaxUpload:      envInt("MAX_UPLOAD_JOBS", 2),
		SyncHours:      envInt("SYNC_INTERVAL_HOURS", 6),
		ImgCacheMaxMB:  envInt("IMG_CACHE_MAX_MB", 2048),
		FFmpegPath:     env("FFMPEG_PATH", "ffmpeg"),
		FFprobePath:    env("FFPROBE_PATH", "ffprobe"),
		YtDlpPath:      env("YTDLP_PATH", "yt-dlp"),
		DownloadRateKB: envInt("DOWNLOAD_RATE_KB", 0),
	}
	if c.SecretKey == "" {
		return nil, fmt.Errorf("缺少 APP_ENCRYPTION_KEY（生成方式：openssl rand -base64 32）")
	}
	for _, d := range []string{c.DataDir, filepath.Join(c.DataDir, "imgcache"),
		filepath.Join(c.DataDir, "tmp")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("创建目录 %s: %w", d, err)
		}
	}
	return c, nil
}

// PlatformProxyFor 返回平台级代理，未配置时回落到全局代理。
//
// 只反映**启动时的环境变量**。界面里能改的代理走 proxying.Resolver（会先读设置表），
// 这里留着是为了兼容老调用点和环境变量兜底。
func (c *Config) PlatformProxyFor(key string) string {
	if p := strings.TrimSpace(c.PlatformProxy[key]); p != "" {
		return p
	}
	return c.GlobalProxy
}

// PlatformProxyAt 只返回平台专用代理（不看全局），供 proxying.Resolver 分层判断用。
func (c *Config) PlatformProxyAt(key string) string {
	return strings.TrimSpace(c.PlatformProxy[key])
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return strings.TrimSpace(v)
	}
	return def
}

func envInt(k string, def int) int {
	if v, ok := os.LookupEnv(k); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

// LoadDotEnv 解析简单的 KEY=VALUE 文件；已存在的环境变量优先。
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if _, exists := os.LookupEnv(k); !exists && v != "" {
			_ = os.Setenv(k, v)
		}
	}
	return sc.Err()
}
