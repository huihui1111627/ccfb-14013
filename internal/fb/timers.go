// Package fb 实现 IEC 61131-3 标准定时与边沿功能块的原生（Go）实例。
//
// 时序以“扫描拍”为最小时间单位，时间累计使用运行时注入的虚拟时钟，
// 因此在加速仿真下行为与真实 PLC 完全一致、可复现。
package fb

import (
	"time"

	"softplc/internal/st"
)

func boolV(b bool) st.Value { return st.BoolVal(b) }

func timeV(d time.Duration) st.Value {
	return st.Value{Type: st.PrimitiveType("TIME"), V: int64(d)}
}

// ---------- TON 接通延时 ----------
//
// 标准行为（IEC 61131-3:2013 §2.5.10）：
//   IN=FALSE 时 Q=FALSE、ET=0；
//   IN=TRUE 时 ET 从 0 开始累计，ET>=PT 后 Q=TRUE，ET 停在 PT；
//   IN 中途断开立即复位 Q=FALSE、ET=0。
type TON struct {
	prevIN bool
	q      bool
	et     time.Duration
}

func (t *TON) Kind() string { return "TON" }

func (t *TON) Execute(in map[string]st.Value, now, dt time.Duration) map[string]st.Value {
	input := in["IN"].AsBool()
	pt := time.Duration(in["PT"].V.(int64))
	if !input {
		t.q = false
		t.et = 0
	} else {
		if !t.prevIN {
			// 本拍刚接通：从 0 起计时
			t.et = 0
		}
		t.et += dt
		if t.et >= pt {
			if pt < 0 {
				t.q = false
			} else {
				t.q = true
			}
			if t.et > pt {
				t.et = pt
			}
		}
	}
	t.prevIN = input
	return map[string]st.Value{"Q": boolV(t.q), "ET": timeV(t.et)}
}

func (t *TON) Snapshot() map[string]st.Value {
	return map[string]st.Value{
		"PREV_IN": boolV(t.prevIN),
		"Q":       boolV(t.q),
		"ET":      timeV(t.et),
	}
}

func (t *TON) Restore(m map[string]st.Value) {
	if v, ok := m["PREV_IN"]; ok {
		t.prevIN = v.AsBool()
	}
	if v, ok := m["Q"]; ok {
		t.q = v.AsBool()
	}
	if v, ok := m["ET"]; ok {
		t.et = time.Duration(v.V.(int64))
	}
}

// ---------- TOF 断开延时 ----------
//
// 标准行为：
//   IN=TRUE 时 Q=TRUE、ET=0；
//   IN 变 FALSE 后 Q 保持 TRUE，ET 累计，ET>=PT 后 Q=FALSE，ET 停在 PT；
//   计时期间 IN 恢复 TRUE 则复位 ET=0，Q 保持 TRUE。
type TOF struct {
	prevIN bool
	q      bool
	et     time.Duration
}

func (t *TOF) Kind() string { return "TOF" }

func (t *TOF) Execute(in map[string]st.Value, now, dt time.Duration) map[string]st.Value {
	input := in["IN"].AsBool()
	pt := time.Duration(in["PT"].V.(int64))
	if input {
		t.q = true
		t.et = 0
	} else {
		if t.prevIN {
			// 本拍刚断开
			t.et = 0
		}
		if t.q {
			t.et += dt
			if t.et >= pt {
				t.q = false
				if t.et > pt {
					t.et = pt
				}
			}
		}
	}
	t.prevIN = input
	return map[string]st.Value{"Q": boolV(t.q), "ET": timeV(t.et)}
}

func (t *TOF) Snapshot() map[string]st.Value {
	return map[string]st.Value{
		"PREV_IN": boolV(t.prevIN),
		"Q":       boolV(t.q),
		"ET":      timeV(t.et),
	}
}

func (t *TOF) Restore(m map[string]st.Value) {
	if v, ok := m["PREV_IN"]; ok {
		t.prevIN = v.AsBool()
	}
	if v, ok := m["Q"]; ok {
		t.q = v.AsBool()
	}
	if v, ok := m["ET"]; ok {
		t.et = time.Duration(v.V.(int64))
	}
}

// ---------- TP 脉冲定时 ----------
//
// 标准行为：
//   IN 出现 FALSE->TRUE 沿时启动一个固定宽度 PT 的脉冲，Q=TRUE；
//   脉冲期间 IN 的变化（含再次上升沿）不影响本次脉冲；
//   ET 在脉冲内累计到 PT，到时 Q=FALSE、ET=PT；IN 为 FALSE 且脉冲结束后 ET=0。
type TP struct {
	prevIN  bool
	running bool
	q       bool
	et      time.Duration
}

func (t *TP) Kind() string { return "TP" }

func (t *TP) Execute(in map[string]st.Value, now, dt time.Duration) map[string]st.Value {
	input := in["IN"].AsBool()
	pt := time.Duration(in["PT"].V.(int64))
	if input && !t.prevIN && !t.running {
		// 触发新脉冲
		t.running = true
		t.q = true
		t.et = 0
	}
	if t.running {
		t.et += dt
		if t.et >= pt {
			t.q = false
			t.running = false
			if t.et > pt {
				t.et = pt
			}
		}
	}
	if !t.running && !input {
		t.et = 0
	}
	t.prevIN = input
	return map[string]st.Value{"Q": boolV(t.q), "ET": timeV(t.et)}
}

func (t *TP) Snapshot() map[string]st.Value {
	return map[string]st.Value{
		"PREV_IN": boolV(t.prevIN),
		"RUNNING": boolV(t.running),
		"Q":       boolV(t.q),
		"ET":      timeV(t.et),
	}
}

func (t *TP) Restore(m map[string]st.Value) {
	if v, ok := m["PREV_IN"]; ok {
		t.prevIN = v.AsBool()
	}
	if v, ok := m["RUNNING"]; ok {
		t.running = v.AsBool()
	}
	if v, ok := m["Q"]; ok {
		t.q = v.AsBool()
	}
	if v, ok := m["ET"]; ok {
		t.et = time.Duration(v.V.(int64))
	}
}

// ---------- R_TRIG 上升沿 ----------
//
// 标准行为：CLK 出现 FALSE->TRUE 的那一拍 Q=TRUE，其余拍 Q=FALSE。
type RTrig struct {
	prevCLK bool
	init    bool
}

func (t *RTrig) Kind() string { return "R_TRIG" }

func (t *RTrig) Execute(in map[string]st.Value, now, dt time.Duration) map[string]st.Value {
	clk := in["CLK"].AsBool()
	q := false
	if t.init {
		q = clk && !t.prevCLK
	}
	t.prevCLK = clk
	t.init = true
	return map[string]st.Value{"Q": boolV(q)}
}

func (t *RTrig) Snapshot() map[string]st.Value {
	return map[string]st.Value{
		"PREV_CLK": boolV(t.prevCLK),
		"INIT":     boolV(t.init),
	}
}

func (t *RTrig) Restore(m map[string]st.Value) {
	if v, ok := m["PREV_CLK"]; ok {
		t.prevCLK = v.AsBool()
	}
	if v, ok := m["INIT"]; ok {
		t.init = v.AsBool()
	} else {
		t.init = true
	}
}

// ---------- F_TRIG 下降沿 ----------
//
// 标准行为：CLK 出现 TRUE->FALSE 的那一拍 Q=TRUE，其余拍 Q=FALSE。
type FTrig struct {
	prevCLK bool
	init    bool
}

func (t *FTrig) Kind() string { return "F_TRIG" }

func (t *FTrig) Execute(in map[string]st.Value, now, dt time.Duration) map[string]st.Value {
	clk := in["CLK"].AsBool()
	q := false
	if t.init {
		q = !clk && t.prevCLK
	}
	t.prevCLK = clk
	t.init = true
	return map[string]st.Value{"Q": boolV(q)}
}

func (t *FTrig) Snapshot() map[string]st.Value {
	return map[string]st.Value{
		"PREV_CLK": boolV(t.prevCLK),
		"INIT":     boolV(t.init),
	}
}

func (t *FTrig) Restore(m map[string]st.Value) {
	if v, ok := m["PREV_CLK"]; ok {
		t.prevCLK = v.AsBool()
	}
	if v, ok := m["INIT"]; ok {
		t.init = v.AsBool()
	} else {
		t.init = true
	}
}

// New 按型号名创建原生功能块实例。
func New(kind string) st.NativeFB {
	switch kind {
	case "TON":
		return &TON{}
	case "TOF":
		return &TOF{}
	case "TP":
		return &TP{}
	case "R_TRIG":
		return &RTrig{}
	case "F_TRIG":
		return &FTrig{}
	}
	return nil
}
