//go:build darwin || linux

package library

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
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

// CheckSpace 检查目录是否存在、可写，并返回可用/总容量。
// 目录不存在但父目录可创建时视为可写（下载前会 MkdirAll）。
func CheckSpace(path string) SpaceInfo {
	info := SpaceInfo{Path: path}
	if path == "" {
		info.Message = "路径为空"
		return info
	}
	if _, err := os.Stat(path); err == nil {
		info.Exists = true
	}
	// 可写校验：能建目录且能落一个临时文件。
	if info.Writable = probeWritable(path); info.Writable {
		info.Message = "可写"
	} else {
		info.Message = "目录不可写"
	}
	target := path
	if !info.Exists {
		target = nearestExisting(path)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(target, &st); err == nil {
		info.FreeBytes = uint64(st.Bavail) * uint64(st.Bsize)
		info.TotalBytes = uint64(st.Blocks) * uint64(st.Bsize)
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

// nearestExisting 向上找到第一个存在的祖先目录，用来估算容量。
func nearestExisting(p string) string {
	cur, err := filepath.Abs(p)
	if err != nil {
		return "/"
	}
	for {
		if _, err := os.Stat(cur); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return cur
		}
		cur = parent
	}
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
