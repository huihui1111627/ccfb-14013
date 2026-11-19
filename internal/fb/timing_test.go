package fb_test

import (
	"testing"
	"time"

	"softplc/internal/fb"
	"softplc/internal/st"
)

// 时序测试统一以 10ms 为扫描拍，逐拍驱动并核对 Q/ET，精确到扫描拍。
const tick = 10 * time.Millisecond

func runFB(t *testing.T, kind string, inSeq []bool, pt time.Duration) ([]bool, []time.Duration) {
	t.Helper()
	inst := fb.New(kind)
	if inst == nil {
		t.Fatalf("未知功能块 %s", kind)
	}
	qs := make([]bool, len(inSeq))
	ets := make([]time.Duration, len(inSeq))
	for i, in := range inSeq {
		now := time.Duration(i) * tick
		inputs := map[string]st.Value{
			"IN":  st.BoolVal(in),
			"PT":  st.Value{Type: st.PrimitiveType("TIME"), V: int64(pt)},
			"CLK": st.BoolVal(in),
		}
		out := inst.Execute(inputs, now, tick)
		qs[i] = out["Q"].AsBool()
		if ev, ok := out["ET"]; ok {
			ets[i] = time.Duration(ev.V.(int64))
		}
	}
	return qs, ets
}

// TON：IN 持续为真，PT 到达那一拍 Q 置位；IN 断开立即复位。
func TestTONTiming(t *testing.T) {
	// IN: 真×5、假×2、真×3；PT=3 拍(30ms)
	in := []bool{true, true, true, true, true, false, false, true, true, true}
	q, et := runFB(t, "TON", in, 3*tick)
	// 拍0: ET=10 Q=false；拍1: ET=20；拍2: ET>=30 Q=true
	wantQ := []bool{false, false, true, true, true, false, false, false, false, true}
	for i := range wantQ {
		if q[i] != wantQ[i] {
			t.Errorf("TON 拍%d Q=%v 期望%v (ET=%s)", i, q[i], wantQ[i], et[i])
		}
	}
	// ET 停在 PT 不超过
	if et[4] != 3*tick {
		t.Errorf("TON ET 应停在 PT=3拍，实际 %s", et[4])
	}
	// IN 断开 ET 立即归零
	if et[5] != 0 {
		t.Errorf("TON 断开后 ET 应归零，实际 %s", et[5])
	}
	// 重新接通从头计时：拍7 ET=10，拍9 ET=30 Q=true
	if et[7] != tick {
		t.Errorf("TON 重新接通首拍 ET 应=10ms，实际 %s", et[7])
	}
}

// TON：PT 恰好等于 N 拍，第 N 拍（从 0 起的第 N-1 个索引之后）置位边界。
func TestTONBoundary(t *testing.T) {
	in := []bool{true, true, true}
	q, _ := runFB(t, "TON", in, 2*tick)
	if q[0] || q[1] != true {
		t.Errorf("TON 边界错误: 拍0应false 拍1应true，得到 %v", q)
	}
}

// TOF：IN 真时 Q 真；IN 断开后 Q 保持，PT 到才变假；计时中恢复 IN 则复位。
func TestTOFTiming(t *testing.T) {
	// 真×2，假×4（PT=3拍），再真
	in := []bool{true, true, false, false, false, false, true, false}
	q, et := runFB(t, "TOF", in, 3*tick)
	wantQ := []bool{true, true, true, true, false, false, true, true}
	for i := range wantQ {
		if q[i] != wantQ[i] {
			t.Errorf("TOF 拍%d Q=%v 期望%v (ET=%s)", i, q[i], wantQ[i], et[i])
		}
	}
	// 拍2(首个假) ET=10，拍4 ET=30，拍5 Q=false ET=PT
	if et[2] != tick || et[4] != 3*tick {
		t.Errorf("TOF ET 累计错误: 拍2=%s 拍4=%s", et[2], et[4])
	}
	// 拍6 IN 恢复真，ET 归零
	if et[6] != 0 {
		t.Errorf("TOF IN 恢复后 ET 应归零，实际 %s", et[6])
	}
}

// TOF：计时期间 IN 短暂恢复，Q 保持且 ET 复位。
func TestTOFRetrigger(t *testing.T) {
	in := []bool{false, true, false, false, true, false, false, false, false}
	q, et := runFB(t, "TOF", in, 3*tick)
	// 初始全假：Q=false；拍1真->Q=true；拍2-3假计时；拍4真复位 ET=0
	if !q[1] {
		t.Error("TOF IN 真时 Q 应为真")
	}
	if et[4] != 0 || !q[4] {
		t.Errorf("TOF 计时中恢复 IN 应 ET=0 Q=true，得到 ET=%s Q=%v", et[4], q[4])
	}
	// 拍5起重新计时，拍7 ET=30，拍8 Q=false
	if q[8] {
		t.Error("TOF 重新延时后 Q 应在拍8为假")
	}
}

// TP：上升沿触发固定宽度脉冲，脉冲期间 IN 变化不影响；ET 到时停 PT。
func TestTPTiming(t *testing.T) {
	// 上升沿在拍0，PT=3拍；拍2 IN 就断开，脉冲仍持续到拍2
	in := []bool{true, true, true, false, false, true, true, true}
	q, et := runFB(t, "TP", in, 3*tick)
	// 拍0触发 Q=true ET=10；拍1 ET=20；拍2 ET=30 Q 转 false
	wantQ := []bool{true, true, false, false, false, true, true, false}
	for i := range wantQ {
		if q[i] != wantQ[i] {
			t.Errorf("TP 拍%d Q=%v 期望%v (ET=%s)", i, q[i], wantQ[i], et[i])
		}
	}
	if et[2] != 3*tick {
		t.Errorf("TP ET 到时应=PT，实际 %s", et[2])
	}
	// 拍5 新上升沿触发第二个脉冲（拍3-4 IN 为假）
	if !q[5] || !q[6] || q[7] {
		t.Errorf("TP 二次脉冲错误: %v", q[5:])
	}
}

// TP：脉冲期间再次上升沿不会重启脉冲。
func TestTPNoRestart(t *testing.T) {
	// 拍0触发，拍1 IN 短暂假、拍2 又真（新沿），但首次脉冲未结束前…
	// 按标准：脉冲 running 期间新沿被忽略。构造 PT=4拍。
	in := []bool{true, false, true, true, true}
	q, _ := runFB(t, "TP", in, 4*tick)
	// 拍0触发 Q=true；拍1 IN假但 running 继续；拍2 新沿被忽略；
	// 拍0 ET=10..拍3 ET=40 到时，拍3 起 Q=false
	wantQ := []bool{true, true, true, false, false}
	for i := range wantQ {
		if q[i] != wantQ[i] {
			t.Errorf("TP 忽略重触发 拍%d Q=%v 期望%v", i, q[i], wantQ[i])
		}
	}
}

// R_TRIG：仅 FALSE->TRUE 那一拍 Q=true。
func TestRTrigTiming(t *testing.T) {
	in := []bool{false, true, true, false, false, true, false}
	q, _ := runFB(t, "R_TRIG", in, 0)
	wantQ := []bool{false, true, false, false, false, true, false}
	for i := range wantQ {
		if q[i] != wantQ[i] {
			t.Errorf("R_TRIG 拍%d Q=%v 期望%v", i, q[i], wantQ[i])
		}
	}
}

// F_TRIG：仅 TRUE->FALSE 那一拍 Q=true。
func TestFTrigTiming(t *testing.T) {
	in := []bool{true, true, false, false, true, false}
	q, _ := runFB(t, "F_TRIG", in, 0)
	wantQ := []bool{false, false, true, false, false, true}
	for i := range wantQ {
		if q[i] != wantQ[i] {
			t.Errorf("F_TRIG 拍%d Q=%v 期望%v", i, q[i], wantQ[i])
		}
	}
}

// 首拍上电：R_TRIG 输入初值真不应当作上升沿（无前一拍 false 记录以外的状态）。
func TestEdgePowerUp(t *testing.T) {
	r := fb.New("R_TRIG")
	out := r.Execute(map[string]st.Value{"CLK": st.BoolVal(true)}, 0, tick)
	if out["Q"].AsBool() {
		t.Error("R_TRIG 上电首拍 CLK=真 不应产生上升沿")
	}
	f := fb.New("F_TRIG")
	out = f.Execute(map[string]st.Value{"CLK": st.BoolVal(false)}, 0, tick)
	if out["Q"].AsBool() {
		t.Error("F_TRIG 上电首拍 CLK=假 不应产生下降沿")
	}
}

// 状态快照与恢复：在线换程序后定时器内部状态必须延续。
func TestSnapshotRestore(t *testing.T) {
	src := fb.New("TON")
	// 跑 2 拍真，ET=20ms 未到 PT
	for i := 0; i < 2; i++ {
		src.Execute(map[string]st.Value{
			"IN": st.BoolVal(true),
			"PT": st.Value{Type: st.PrimitiveType("TIME"), V: int64(4 * tick)},
		}, time.Duration(i)*tick, tick)
	}
	snap := src.Snapshot()
	dst := fb.New("TON")
	dst.Restore(snap)
	// 再跑 2 拍，第 4 拍应到 PT 置位
	var q bool
	for i := 2; i < 4; i++ {
		out := dst.Execute(map[string]st.Value{
			"IN": st.BoolVal(true),
			"PT": st.Value{Type: st.PrimitiveType("TIME"), V: int64(4 * tick)},
		}, time.Duration(i)*tick, tick)
		q = out["Q"].AsBool()
	}
	if !q {
		t.Error("TON 快照恢复后累计计时未延续，应在第4拍置位")
	}
}
