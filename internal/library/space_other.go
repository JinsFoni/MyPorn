//go:build !(darwin || linux)

package library

import (
	"fmt"
	"os"
)

// SpaceInfo 描述一个目录所在文件系统的容量。
type SpaceInfo struct {
	Path       string `json:"path"`
	Exists     bool   `json:"exists"`
	Writable   bool   `json:"writable"`
	FreeBytes  uint64 `json:"freeBytes"`
	TotalBytes uint64 `json:"totalBytes"`
	Message    string `json:"message"`
}

// CheckSpace 在非 unix 平台上退化为仅做存在性与可写检查。
func CheckSpace(path string) SpaceInfo {
	info := SpaceInfo{Path: path}
	if path == "" {
		info.Message = "路径为空"
		return info
	}
	if _, err := os.Stat(path); err == nil {
		info.Exists = true
	}
	info.Writable = probeWritable(path)
	if info.Writable {
		info.Message = "可写（本平台不报告剩余空间）"
	} else {
		info.Message = "目录不可写"
	}
	return info
}

func probeWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".myporn-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return true
}

// HumanBytes 把字节数格式化成便于展示的字符串。
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	if exp >= len(units) {
		exp = len(units) - 1
	}
	return fmt.Sprintf("%.1f %s", float64(n)/float64(div), units[exp])
}

// EnsureWritable 确保目录存在且可写，否则返回错误（下载前调用）。
func EnsureWritable(dir string) error {
	if dir == "" {
		return errPath("库目录为空")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".myporn-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return nil
}
