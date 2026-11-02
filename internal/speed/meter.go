// Package speed 把「网速」算成**最近几秒的平均**，而不是两次快照之间的瞬时差。
//
// 为什么要有这个包（2026-11-03 用户报「下载里的网速一会几 MB/s，一会几 KB/s」）：
// 两条下载链路原来各自算速度，但都是「这一拍的字节数 - 上一拍」除以那两拍之间的时间，
// 而字节数是**成块跳变**的——
//   - 自研 HLS 下载器：一个分片整段写完才加一次字节；
//   - ffmpeg 直拉：total_size 一个进度块一跳（几 MB 一级）；
//   - yt-dlp：先下视频轨再下音频轨，累计字节还会回零。
//
// 0.7 秒的心跳落在「两块之间」就算出 0，落在一块刚落地就算出 23 MB/s，界面上就是心电图。
// 换成 3 秒滑窗平均（窗口左端随时间滑动，不是固定分桶）后，同样的链路报出来是稳定的数字，
// 而真掉速 3 秒内也能看出来。
package speed

import "time"

// DefaultWindow 是默认的平均窗口。3 秒的取舍：更短回到抖动，更长会让「真变慢」迟 3 秒才显示。
const DefaultWindow = 3 * time.Second

// DefaultWarmUp 是「至少要攒够多久的数据才报速度」。
//
// 为什么需要（2026-11-03 实测 xhamster 那条 47 分钟的片子）：头一个分片落地时窗口里只有
// 一两百毫秒的数据，报出来是 2.2 MB/s；两个心跳之后 100 个分片一起落地，同一套算法又报
// 57 MB/s——数字本身都没算错，但那 26 倍落差看着就是「网速坏了」。攒够 1.5 秒再开口，
// 界面上就是「先没有速度」→「稳定在 45–57 MB/s」。
const DefaultWarmUp = 1500 * time.Millisecond

type sample struct {
	at    time.Time
	bytes int64
}

// Meter 是「累计字节数 → 速率」的滑窗平均器。零值不可用，用 New。
// 非并发安全：调用方自己加锁（两条链路的调用点都来自多个协程）。
type Meter struct {
	window  time.Duration
	warmUp  time.Duration
	samples []sample
	last    int64
}

// New 造一个窗口为 window 的度量器；window <= 0 时用 DefaultWindow。
func New(window time.Duration) *Meter {
	if window <= 0 {
		window = DefaultWindow
	}
	return &Meter{window: window, warmUp: DefaultWarmUp}
}

// Update 吃掉一个（时刻，累计字节）采样，返回此刻该显示的速度（字节/秒）。
//
// 累计字节**回退**（yt-dlp 换轨道、分片重试重算）时把窗口清空重新起算，
// 不然后面几十秒都会被那个负数拖成 0。
func (m *Meter) Update(now time.Time, bytes int64) int64 {
	// 还没有字节：窗口不该开始计时（否则第一块数据落地时窗口里全是「空等」）。
	if bytes <= 0 {
		m.samples = m.samples[:0]
		m.last = 0
		return 0
	}
	// 累计字节回退（yt-dlp 换轨道、分片重试重算）→ 清空重算，不然后面几十秒都被拖成 0。
	if n := len(m.samples); n > 0 && bytes < m.samples[n-1].bytes {
		m.samples = m.samples[:0]
		m.last = 0
	}
	m.samples = append(m.samples, sample{at: now, bytes: bytes})
	// 从左往右丢：留下第一个「还不算太旧」的样本当分母，它之前的都不要了
	for len(m.samples) > 2 && now.Sub(m.samples[1].at) >= m.window {
		m.samples = m.samples[1:]
	}
	dt := now.Sub(m.samples[0].at)
	if dt < m.warmUp {
		// 数据太少，这时候的数字只会误导（见 DefaultWarmUp 的注释）
		m.last = 0
		return 0
	}
	m.last = int64(float64(bytes-m.samples[0].bytes) / dt.Seconds())
	return m.last
}
