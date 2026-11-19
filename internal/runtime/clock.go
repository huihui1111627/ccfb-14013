package runtime

import "time"

// Clock 时钟抽象。虚拟时钟由扫描循环显式推进，可任意加速；
// 真实时钟读墙钟，用于接真实硬件。
type Clock interface {
	Now() time.Duration
	Advance(d time.Duration)
	Real() bool
}

// VirtualClock 虚拟时钟：时间只随扫描推进，完全确定可复现。
type VirtualClock struct {
	t time.Duration
}

func NewVirtualClock() *VirtualClock { return &VirtualClock{} }

func (c *VirtualClock) Now() time.Duration     { return c.t }
func (c *VirtualClock) Advance(d time.Duration) { c.t += d }
func (c *VirtualClock) Real() bool             { return false }

// RealClock 墙钟（启动以来的单调时间）。
type RealClock struct {
	start time.Time
}

func NewRealClock() *RealClock { return &RealClock{start: time.Now()} }

func (c *RealClock) Now() time.Duration { return time.Since(c.start) }
func (c *RealClock) Advance(d time.Duration) {
	// 真实时钟不能被推进；忙等到对应时刻（由调度循环决定是否使用）
	time.Sleep(d)
}
func (c *RealClock) Real() bool { return true }
