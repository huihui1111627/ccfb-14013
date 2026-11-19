package runtime_test

import (
	"testing"
	"time"

	"softplc/internal/runtime"
)

// 强制：强制中的输入点，程序读到的是强制值而非物理值；输出强制值覆盖程序输出。
func TestForcing(t *testing.T) {
	src := `
VAR_GLOBAL
  IN1  : INT;
  OUT1 : BOOL;
END_VAR
PROGRAM CTRL
BEGIN
  IF IN1 > 10 THEN OUT1 := TRUE; ELSE OUT1 := FALSE; END_IF;
END_PROGRAM
`
	cfg := runtime.Config{
		RootProgram: "CTRL", Log: &testLog{},
		Tasks: []runtime.TaskConfig{
			{Name: "CTRL", POU: "CTRL", Period: 10 * time.Millisecond, Priority: 5,
				Watchdog: time.Second, MaxStmts: 100000},
		},
		Points: []runtime.PointConfig{
			{Name: "IN1", Direction: runtime.DirInput, SafeValue: "0"},
			{Name: "OUT1", Direction: runtime.DirOutput, SafeValue: "FALSE"},
		},
	}
	ctl := runtime.New(cfg, runtime.NewVirtualClock())
	if err := ctl.LoadProgram(map[string]string{"p.st": src}); err != nil {
		t.Fatal(err)
	}
	// 物理输入 5（程序会算 OUT1=FALSE）
	if err := ctl.IO().SetPhysicalInput("IN1", intVal(5)); err != nil {
		t.Fatal(err)
	}
	// 强制输入为 99（>10，程序应看到 99 而算 OUT1=TRUE）
	if err := ctl.IO().Force("IN1", "99"); err != nil {
		t.Fatal(err)
	}
	if err := ctl.Tick(10 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	f := ctl.GlobalFrame()
	if got := f.Slots[f.Vars["IN1"].Base].AsInt(); got != 99 {
		t.Errorf("强制输入后程序应读到99，实际 %d", got)
	}
	if !ctl.IO().PhysicalOutput("OUT1").AsBool() {
		t.Error("基于强制输入，OUT1 应为 TRUE")
	}
	// 强制输出为 FALSE，应覆盖程序结果
	if err := ctl.IO().Force("OUT1", "FALSE"); err != nil {
		t.Fatal(err)
	}
	if err := ctl.Tick(10 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if ctl.IO().PhysicalOutput("OUT1").AsBool() {
		t.Error("强制输出 FALSE 应覆盖程序输出")
	}
	if len(ctl.IO().ForcedPoints()) != 2 {
		t.Error("应有两个强制点")
	}
	// 解除输入强制，程序恢复读物理值 5
	if err := ctl.IO().Unforce("IN1"); err != nil {
		t.Fatal(err)
	}
	if err := ctl.Tick(10 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if got := f.Slots[f.Vars["IN1"].Base].AsInt(); got != 5 {
		t.Errorf("解除强制后应恢复物理值5，实际 %d", got)
	}
}

// 在线换程序：同名同类型值带过去，类型变化按规则转换，新增变量取初值，定时器状态保留。
func TestOnlineSwapMigration(t *testing.T) {
	v1 := `
VAR_GLOBAL
  A : INT := 7;
  B : INT;
  T : TON;
  Q : BOOL;
END_VAR
PROGRAM CTRL
BEGIN
  B := A;
  T(IN := TRUE, PT := T#10s, Q => Q);
END_PROGRAM
`
	// v2：A 改 LREAL（类型迁移），新增 C，B 保持 INT，定时器同名同型
	v2 := `
VAR_GLOBAL
  A : LREAL := 0.0;
  B : INT;
  C : DINT := 123;
  T : TON;
  Q : BOOL;
END_VAR
PROGRAM CTRL
BEGIN
  B := B + 1;
  T(IN := TRUE, PT := T#10s, Q => Q);
END_PROGRAM
`
	cfg := runtime.Config{
		RootProgram: "CTRL", Log: &testLog{},
		Tasks: []runtime.TaskConfig{
			{Name: "CTRL", POU: "CTRL", Period: 10 * time.Millisecond, Priority: 5,
				Watchdog: time.Second, MaxStmts: 100000},
		},
	}
	ctl := runtime.New(cfg, runtime.NewVirtualClock())
	if err := ctl.LoadProgram(map[string]string{"v1.st": v1}); err != nil {
		t.Fatal(err)
	}
	// 跑 3 拍让 TON 累计 30ms
	for i := 0; i < 3; i++ {
		if err := ctl.Tick(10 * time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	// 两拍之间换程序
	ctl.RequestSwap(&runtime.SwapRequest{
		Files:       map[string]string{"v2.st": v2},
		RootProgram: "CTRL",
	})
	if err := ctl.Tick(10 * time.Millisecond); err != nil {
		t.Fatalf("换程序: %v", err)
	}
	rep := ctl.SwapReport()
	if rep == nil {
		t.Fatal("缺少换程序报告")
	}
	f := ctl.GlobalFrame()
	// A: INT 7 -> LREAL 7.0
	if got := f.Slots[f.Vars["A"].Base].AsReal(); got != 7.0 {
		t.Errorf("类型迁移 A 应为 7.0，实际 %v", got)
	}
	// B: 同名同型，带值（v1 每拍 B=A=7）
	if got := f.Slots[f.Vars["B"].Base].AsInt(); got != 7 {
		t.Errorf("同名同型 B 应带值7，实际 %d", got)
	}
	// C: 新增变量取初值
	if got := f.Slots[f.Vars["C"].Base].AsInt(); got != 123 {
		t.Errorf("新增 C 应取初值123，实际 %d", got)
	}
	// 转换列表应含 A
	foundConv := false
	for _, n := range rep.Converted {
		if n == "A" {
			foundConv = true
		}
	}
	if !foundConv {
		t.Errorf("A 应在类型转换列表，实际转换=%v", rep.Converted)
	}
	// 再跑一拍，TON 状态延续不应报错
	if err := ctl.Tick(10 * time.Millisecond); err != nil {
		t.Fatalf("换程序后运行: %v", err)
	}
}

// 换程序失败（新程序有类型错误）时保留旧程序继续运行。
func TestSwapFailureKeepsOld(t *testing.T) {
	v1 := `VAR_GLOBAL A:INT; END_VAR
PROGRAM CTRL BEGIN A := 1; END_PROGRAM`
	bad := `VAR_GLOBAL A:INT; END_VAR
PROGRAM CTRL BEGIN A := TRUE; END_PROGRAM` // 类型不匹配
	cfg := runtime.Config{
		RootProgram: "CTRL", Log: &testLog{},
		Tasks: []runtime.TaskConfig{
			{Name: "CTRL", POU: "CTRL", Period: 10 * time.Millisecond, Priority: 5,
				Watchdog: time.Second, MaxStmts: 100000},
		},
	}
	ctl := runtime.New(cfg, runtime.NewVirtualClock())
	if err := ctl.LoadProgram(map[string]string{"v1.st": v1}); err != nil {
		t.Fatal(err)
	}
	ctl.RequestSwap(&runtime.SwapRequest{Files: map[string]string{"bad.st": bad}, RootProgram: "CTRL"})
	err := ctl.Tick(10 * time.Millisecond)
	if err == nil {
		t.Error("坏程序应导致换程序返回错误")
	}
	// 旧程序仍可正常运行
	if e := ctl.Tick(10 * time.Millisecond); e != nil {
		t.Errorf("换程序失败后旧程序应继续运行: %v", e)
	}
}
