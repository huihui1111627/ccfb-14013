// simtest 在虚拟时钟上把软控制器与泵站过程仿真闭环联调，
// 支持：一个月虚拟时间连跑、中途在线换程序、强制点、看门狗演示、抖动统计。
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"softplc/internal/plant"
	"softplc/internal/runtime"
	"softplc/internal/st"
)

const (
	scanTick   = 1 * time.Second // 基础扫描节拍
	ctrlPeriod = 1 * time.Second
	housePeriod = 5 * time.Second
)

type sim struct {
	ctl *runtime.Controller
	stn *plant.Station
}

func buildConfig() runtime.Config {
	return runtime.Config{
		RootProgram: "CTRL",
		Tasks: []runtime.TaskConfig{
			{Name: "CTRL", POU: "CTRL", Period: ctrlPeriod, Priority: 20,
				Watchdog: 200 * time.Millisecond, MaxStmts: 2_000_000},
			{Name: "HOUSE", POU: "HOUSE", Period: housePeriod, Priority: 10,
				Watchdog: 200 * time.Millisecond, MaxStmts: 2_000_000},
		},
		Points: []runtime.PointConfig{
			{Name: "LEVEL_M", Direction: runtime.DirInput, SafeValue: "0"},
			{Name: "PUMP1", Direction: runtime.DirOutput, SafeValue: "FALSE"},
			{Name: "PUMP2", Direction: runtime.DirOutput, SafeValue: "FALSE"},
			{Name: "VIN_CMD", Direction: runtime.DirOutput, SafeValue: "0"},
			{Name: "VOUT_CMD", Direction: runtime.DirOutput, SafeValue: "0"},
		},
		Log: consoleLog{},
	}
}

type consoleLog struct{}

func (consoleLog) Logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func newSim(programFile string) (*sim, error) {
	src, err := os.ReadFile(programFile)
	if err != nil {
		return nil, err
	}
	clk := runtime.NewVirtualClock()
	ctl := runtime.New(buildConfig(), clk)
	if err := ctl.LoadProgram(map[string]string{programFile: string(src)}); err != nil {
		return nil, err
	}
	stn := plant.New(plant.Config{
		Seed: 20261120, DT: scanTick.Seconds(),
		TankArea: 10.0, TankMin: 0.0, TankMax: 10.0,
		NoiseStd: 0.08, GlitchProb: 0.01, GlitchAmp: 0.6, StuckProb: 0.002,
	})
	return &sim{ctl: ctl, stn: stn}, nil
}

// runOneTick 跑一拍：拍前把传感器读入过程输入，拍后把输出命令送给过程。
func (s *sim) runOneTick() error {
	// 过程输入（仿真器 -> 控制器物理输入）
	if err := s.ctl.IO().SetPhysicalInput("LEVEL_M",
		st.RealVal(st.PrimitiveType("LREAL"), s.stn.TrueLevel())); err != nil {
		return err
	}
	if err := s.ctl.Tick(scanTick); err != nil {
		return err
	}
	// 输出命令（控制器物理输出 -> 过程）。强制点由 IO 层已覆盖。
	cmd := plant.Commands{
		Pump1On: s.ctl.IO().PhysicalOutput("PUMP1").AsBool(),
		Pump2On: s.ctl.IO().PhysicalOutput("PUMP2").AsBool(),
		InOpen:  s.ctl.IO().PhysicalOutput("VIN_CMD").AsReal(),
		OutOpen: s.ctl.IO().PhysicalOutput("VOUT_CMD").AsReal(),
	}
	s.stn.Step(cmd)
	return nil
}

func (s *sim) globalReal(name string) float64 {
	f := s.ctl.GlobalFrame()
	b := f.Vars[name]
	return f.Slots[b.Base].AsReal()
}

func main() {
	var (
		duration = flag.Duration("dur", 30*24*time.Hour, "虚拟运行时长")
		prog     = flag.String("prog", "programs/pump_v1.st", "初始程序")
		swapAt   = flag.Duration("swap1", 10*24*time.Hour, "第一次在线换程序时刻")
		swapAt2  = flag.Duration("swap2", 18*24*time.Hour, "第二次")
		swapAt3  = flag.Duration("swap3", 25*24*time.Hour, "第三次")
		report   = flag.String("report", "", "验收报告输出文件")
	)
	flag.Parse()

	s, err := newSim(*prog)
	if err != nil {
		fmt.Fprintln(os.Stderr, "初始化失败:", err)
		os.Exit(1)
	}

	// 验收统计
	minLevel, maxLevel := s.stn.TrueLevel(), s.stn.TrueLevel()
	violations := 0
	swaps := []time.Duration{*swapAt, *swapAt2, *swapAt3}
	swapFiles := []string{"programs/pump_v2.st", "programs/pump_v3.st", "programs/pump_v1.st"}
	swapIdx := 0
	reported := map[*runtime.SwapReport]bool{}

	deadline := s.ctl.Clock().Now() + *duration
	ticks := 0
	for s.ctl.Clock().Now() < deadline {
		// 到点发起在线换程序（两拍之间由控制器原子完成）
		if swapIdx < len(swaps) && s.ctl.Clock().Now() >= swaps[swapIdx] {
			f := swapFiles[swapIdx]
			src, _ := os.ReadFile(f)
			s.ctl.RequestSwap(&runtime.SwapRequest{
				Files:       map[string]string{f: string(src)},
				RootProgram: "CTRL",
			})
			swapIdx++
		}
		if err := s.runOneTick(); err != nil {
			fmt.Fprintln(os.Stderr, "运行失败:", err)
			os.Exit(1)
		}
		// 换程序报告（只在产生新报告时打印一次）
		if rep := s.ctl.SwapReport(); rep != nil && !reported[rep] {
			reported[rep] = true
			fmt.Fprintf(os.Stderr, "[换程序报告] 带值%d 转换%d 重置%d 新增%d 移除%d FB状态%d\n",
				len(rep.Carried), len(rep.Converted), len(rep.Reset),
				len(rep.New), len(rep.Dropped), len(rep.FBState))
		}
		// 液位判据（用真实液位，不用含噪读数）
		lv := s.stn.TrueLevel()
		if lv < minLevel {
			minLevel = lv
		}
		if lv > maxLevel {
			maxLevel = lv
		}
		// 允许范围 1.0..9.0（物理 0..10，留安全裕度）
		if lv < 1.0 || lv > 9.0 {
			violations++
		}
		ticks++
	}

	out := os.Stdout
	if *report != "" {
		f, err := os.Create(*report)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer f.Close()
		out = f
	}
	printReport(out, s, ticks, minLevel, maxLevel, violations)
}

func printReport(out *os.File, s *sim, ticks int, minL, maxL float64, violations int) {
	fmt.Fprintln(out, "================ 泵站闭环仿真验收报告 ================")
	fmt.Fprintf(out, "虚拟扫描拍数: %d (%.1f 天)\n", ticks, float64(ticks)/86400)
	fmt.Fprintf(out, "真实液位范围: [%.3f, %.3f] m (物理 0..10, 允许 1..9)\n", minL, maxL)
	fmt.Fprintf(out, "液位越限拍数: %d %s\n", violations, pass(violations == 0))
	fmt.Fprintf(out, "泵累计启动次数: %d %s\n", s.stn.PumpStartCount(),
		pass(s.stn.PumpStartCount() < ticks/10))
	fmt.Fprintln(out, "---- 任务耗时与抖动 ----")
	for _, t := range s.ctl.TaskSnapshots() {
		fmt.Fprintf(out, "任务 %-6s 优先级%3d 周期%4s 执行拍数%6d 最近%10s 最大%10s 平均%10s 看门狗%3d\n",
			t.Name, t.Priority, t.Period, t.Runs, t.LastExec, t.MaxExec, t.MeanExec, t.Deadlines)
		fmt.Fprintf(out, "         唤醒抖动(实际-理想到期): 最小%10s 最大%10s 均值%10s 标准差%10s 样本%d（虚拟时钟下确定性为0）\n",
			t.JitterMin, t.JitterMax, t.JitterMean, t.JitterStd, t.JitterCount)
	}
	fmt.Fprintln(out, "---- 强制点 ----")
	fp := s.ctl.IO().ForcedPoints()
	if len(fp) == 0 {
		fmt.Fprintln(out, "（无强制点）")
	}
	for _, p := range fp {
		fmt.Fprintf(out, "【强制】%s = %s\n", p.Cfg.Name, st.FormatValue(p.ForceVal))
	}
	fmt.Fprintln(out, "======================================================")
}

func pass(ok bool) string {
	if ok {
		return "[通过]"
	}
	return "[不通过]"
}
