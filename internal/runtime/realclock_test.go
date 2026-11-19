package runtime_test

import (
	"testing"
	"time"

	"softplc/internal/runtime"
)

// 真实时钟下任务唤醒时刻存在自然抖动，统计应能测到非零执行耗时。
func TestRealClockExecCost(t *testing.T) {
	if testing.Short() {
		t.Skip("真实时钟测试默认跳过")
	}
	src := `
VAR_GLOBAL C:DINT; END_VAR
PROGRAM CTRL
BEGIN C := C + 1; END_PROGRAM
`
	cfg := runtime.Config{
		RootProgram: "CTRL",
		Log:         &testLog{},
		Tasks: []runtime.TaskConfig{
			{Name: "CTRL", POU: "CTRL", Period: time.Millisecond, Priority: 5,
				Watchdog: time.Second, MaxStmts: 100000},
		},
	}
	ctl := runtime.New(cfg, runtime.NewRealClock())
	if err := ctl.LoadProgram(map[string]string{"p.st": src}); err != nil {
		t.Fatal(err)
	}
	// 真实时钟下手动驱动若干拍（不睡眠，仅验证耗时被统计）
	for i := 0; i < 50; i++ {
		if err := ctl.Tick(time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	snaps := ctl.TaskSnapshots()
	if len(snaps) != 1 || snaps[0].Runs == 0 {
		t.Fatal("任务未执行")
	}
	if snaps[0].MaxExec <= 0 {
		t.Error("真实时钟下最大执行耗时应大于 0")
	}
	t.Logf("真实时钟执行耗时: 最近=%s 最大=%s 平均=%s",
		snaps[0].LastExec, snaps[0].MaxExec, snaps[0].MeanExec)
}
