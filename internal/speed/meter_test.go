package speed

import (
	"testing"
	"time"
)

// mb 把「多少 MB/s」换成字节/秒，写在断言里比一堆移位好读。
func mb(n float64) int64 { return int64(n * float64(1<<20)) }

// TestMeterAveragesOutStaircaseTraffic 台阶状的累计字节（分片整块落地 / ffmpeg 一个进度块跳几 MB）
// 不该让速度跟着跳。旧算法（瞬时差 / 0.7 秒心跳）在这组输入上会算出 0 与 11 MB/s 交替。
func TestMeterAveragesOutStaircaseTraffic(t *testing.T) {
	m := New(3 * time.Second)
	t0 := time.Unix(0, 0)
	m.Update(t0, 0)
	jumps := []int64{0, 8 << 20, 0, 0, 8 << 20, 0, 0, 0, 8 << 20, 0, 0, 8 << 20}
	var cum int64
	var got []int64
	for i, j := range jumps {
		cum += j
		got = append(got, m.Update(t0.Add(time.Duration(i+1)*700*time.Millisecond), cum))
	}
	t.Logf("台阶流量：%v", got)
	// 第一条数据落地后的 1.5 秒内一律报 0（= 「还看不出」），别拿半截窗口的数字骗人
	for i := 0; i < 4; i++ {
		if got[i] != 0 {
			t.Errorf("预热期第 %d 个采样应是 0（不够 1.5 秒数据），得到 %d", i, got[i])
		}
	}
	for i, v := range got[4:] { // 之后进入正式窗口
		if v == 0 {
			t.Fatalf("数据一直在流却报 0：%v", got)
		}
		if i > 0 && float64(v)/float64(got[4+i-1]) > 2.2 {
			t.Errorf("相邻两拍差太多：%v", got)
		}
	}
}

// TestMeterFollowsRealSlowdown 均匀 5 MB/s 的链路要报 5 MB/s 上下；降到 1 MB/s 后 3 秒内跟上。
func TestMeterFollowsRealSlowdown(t *testing.T) {
	m := New(3 * time.Second)
	t0 := time.Unix(0, 0)
	var bytes int64
	step := func(i int, perSec int64) int64 {
		bytes += perSec * 700 / 1000
		return m.Update(t0.Add(time.Duration(i)*700*time.Millisecond), bytes)
	}
	var fast []int64
	for i := 1; i <= 8; i++ {
		fast = append(fast, step(i, 5<<20))
	}
	got := fast[len(fast)-1]
	if got < mb(4.4) || got > mb(5.6) {
		t.Errorf("稳定 5 MB/s 应报 5 MB/s 左右，报成 %d（%v）", got, fast)
	}
	var slow int64
	for i := 9; i <= 16; i++ {
		slow = step(i, 1<<20)
	}
	if slow < mb(0.8) || slow > mb(1.2) {
		t.Errorf("降到 1 MB/s 后应跟上，报成 %d", slow)
	}
}

// TestMeterResetsWhenBytesGoBackwards yt-dlp 下完视频轨换音频轨时累计字节归零，不能被当成「掉到 0」。
func TestMeterResetsWhenBytesGoBackwards(t *testing.T) {
	m := New(3 * time.Second)
	t0 := time.Unix(0, 0)
	var got int64
	for i := 1; i <= 5; i++ {
		got = m.Update(t0.Add(time.Duration(i)*700*time.Millisecond), int64(i)*3<<20)
	}
	if got <= 0 {
		t.Fatalf("回退前应算出正速度，得到 %d", got)
	}
	// 换轨：累计字节从 0 重新开始，随后又稳定在 2 MB/s
	base := 5 * 700 * time.Millisecond
	m.Update(t0.Add(base+700*time.Millisecond), 0)
	var last int64
	for i := 1; i <= 6; i++ {
		last = m.Update(t0.Add(base+time.Duration(i+1)*700*time.Millisecond), int64(i)*1400<<10)
	}
	if last < mb(1.5) || last > mb(2.5) {
		t.Errorf("换轨后应报 ~2 MB/s，得到 %d", last)
	}
}
