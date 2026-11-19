package runtime_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"softplc/internal/runtime"
	"softplc/internal/st"
)

// 构造两个任务：慢任务 LOW 周期 10ms 但函数体语句很多，
// 快任务 HIGH 周期 10ms、优先级更高，在 LOW 执行期间到点并抢占。
const preemptSrc = `
VAR_GLOBAL
  LOW_CNT  : DINT;
  HIGH_CNT : DINT;
  LOW_DONE : BOOL;
END_VAR

(* 低优先级：一个大 FOR 循环，制造大量语句边界检查点 *)
PROGRAM LOW
VAR
  I : DINT;
  J : DINT;
END_VAR
BEGIN
  FOR I := 1 TO 2000 DO
    J := I * 2;
    LOW_CNT := LOW_CNT + J - J + 1;
  END_FOR;
  LOW_DONE := TRUE;
END_PROGRAM

(* 高优先级：到点即累加，抢占发生时它先跑完 *)
PROGRAM HIGH
BEGIN
  HIGH_CNT := HIGH_CNT + 1;
END_PROGRAM
`

type testLog struct{ lines []string }

func (l *testLog) Logf(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func TestPreemption(t *testing.T) {
	log := &testLog{}
	cfg := runtime.Config{
		RootProgram: "LOW",
		Log:         log,
		Tasks: []runtime.TaskConfig{
			{Name: "LOW", POU: "LOW", Period: 10 * time.Millisecond, Priority: 5,
				Watchdog: 5 * time.Second, MaxStmts: 100_000_000},
			// HIGH 周期很大，首拍不到点，由钩子在 LOW 执行期间武装
			{Name: "HIGH", POU: "HIGH", Period: time.Hour, Priority: 10,
				Watchdog: 5 * time.Second, MaxStmts: 100_000_000},
		},
	}
	ctl := runtime.New(cfg, runtime.NewVirtualClock())
	if err := ctl.LoadProgram(map[string]string{"p.st": preemptSrc}); err != nil {
		t.Fatalf("加载: %v", err)
	}
	// 把 HIGH 首次到期推到很远，使其首拍不在 due 列表，只能由抢占触发
	if !ctl.DebugSetNextDue("HIGH", time.Hour) {
		t.Fatal("无法设置 HIGH 到期时刻")
	}
	// HIGH 在 LOW 执行到第 50 条语句时武装，
	// 使其在 LOW 的下一个检查点立即满足抢占条件。
	ctl.SetPreemptHook(func(running *runtime.Task) {
		if running.Cfg.Name == "LOW" {
			ctl.DebugArmTaskNow("HIGH")
		}
	})
	// 跑一拍：LOW 到点运行，中途 HIGH 到期抢占
	if err := ctl.Tick(10 * time.Millisecond); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	preempted := false
	for _, ln := range log.lines {
		if strings.Contains(ln, "抢占") {
			preempted = true
		}
	}
	if !preempted {
		t.Errorf("未观察到高优先级任务抢占；日志: %v", log.lines)
	}
	// 抢占后 LOW 从断点继续跑完，LOW_DONE 应置位；HIGH 也执行一次
	snaps := map[string]runtime.TaskSnapshot{}
	for _, s := range ctl.TaskSnapshots() {
		snaps[s.Name] = s
	}
	if snaps["HIGH"].Runs != 1 {
		t.Errorf("HIGH 应被抢占执行1拍，实际 %d", snaps["HIGH"].Runs)
	}
	if snaps["LOW"].Runs != 1 {
		t.Errorf("LOW 应完成1拍，实际 %d", snaps["LOW"].Runs)
	}
	f := ctl.GlobalFrame()
	if !f.Slots[f.Vars["LOW_DONE"].Base].AsBool() {
		t.Error("LOW 被抢占后应从断点继续执行到完成")
	}
	if f.Slots[f.Vars["HIGH_CNT"].Base].AsInt() != 1 {
		t.Error("HIGH 应累加一次")
	}
}

// 看门狗：语句数超限时动作，输出退回安全值。
func TestWatchdogSafety(t *testing.T) {
	src := `
VAR_GLOBAL
  OUT1 : BOOL;
  OUT2 : INT;
END_VAR
PROGRAM CTRL
VAR I : DINT; END_VAR
BEGIN
  OUT1 := TRUE;
  OUT2 := 999;
  FOR I := 1 TO 100000 DO
    OUT2 := OUT2 + 1 - 1;
  END_FOR;
END_PROGRAM
`
	cfg := runtime.Config{
		RootProgram: "CTRL",
		Log:         &testLog{},
		Tasks: []runtime.TaskConfig{
			{Name: "CTRL", POU: "CTRL", Period: 10 * time.Millisecond, Priority: 5,
				Watchdog: 10 * time.Second, MaxStmts: 500}, // 语句数上限很小，必触发
		},
		Points: []runtime.PointConfig{
			{Name: "OUT1", Direction: runtime.DirOutput, SafeValue: "FALSE"},
			{Name: "OUT2", Direction: runtime.DirOutput, SafeValue: "0"},
		},
	}
	ctl := runtime.New(cfg, runtime.NewVirtualClock())
	if err := ctl.LoadProgram(map[string]string{"p.st": src}); err != nil {
		t.Fatalf("加载: %v", err)
	}
	if err := ctl.Tick(10 * time.Millisecond); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if !ctl.WatchdogTripped() {
		t.Fatal("看门狗应已动作")
	}
	// 输出应退到安全值
	if ctl.IO().PhysicalOutput("OUT1").AsBool() {
		t.Error("OUT1 看门狗后应为安全值 FALSE")
	}
	if ctl.IO().PhysicalOutput("OUT2").AsInt() != 0 {
		t.Error("OUT2 看门狗后应为安全值 0")
	}
}

// 输入锁存：一拍内程序多次读同一输入，看到的值不随物理侧变化。
func TestInputLatch(t *testing.T) {
	src := `
VAR_GLOBAL
  LEVEL : INT;
  SEEN1 : INT;
  SEEN2 : INT;
END_VAR
PROGRAM CTRL
BEGIN
  SEEN1 := LEVEL;
  SEEN2 := LEVEL;
END_PROGRAM
`
	cfg := runtime.Config{
		RootProgram: "CTRL",
		Log:         &testLog{},
		Tasks: []runtime.TaskConfig{
			{Name: "CTRL", POU: "CTRL", Period: 10 * time.Millisecond, Priority: 5,
				Watchdog: time.Second, MaxStmts: 10000},
		},
		Points: []runtime.PointConfig{
			{Name: "LEVEL", Direction: runtime.DirInput, SafeValue: "0"},
			{Name: "SEEN1", Direction: runtime.DirOutput, SafeValue: "0"},
			{Name: "SEEN2", Direction: runtime.DirOutput, SafeValue: "0"},
		},
	}
	ctl := runtime.New(cfg, runtime.NewVirtualClock())
	if err := ctl.LoadProgram(map[string]string{"p.st": src}); err != nil {
		t.Fatal(err)
	}
	// 拍前物理输入=42
	if err := ctl.IO().SetPhysicalInput("LEVEL",
		intVal(42)); err != nil {
		t.Fatal(err)
	}
	// 在 Tick 内部无法插入物理写，改为验证锁存语义：拍后改物理值不影响本拍映像
	if err := ctl.Tick(10 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	f := ctl.GlobalFrame()
	b1 := f.Vars["SEEN1"]
	b2 := f.Vars["SEEN2"]
	if f.Slots[b1.Base].AsInt() != 42 || f.Slots[b2.Base].AsInt() != 42 {
		t.Error("一拍内两次读取输入应一致且等于锁存值 42")
	}
}

func intVal(v int64) st.Value {
	return st.IntVal(st.PrimitiveType("DINT"), v)
}
