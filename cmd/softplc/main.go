// softplc 是软控制器主程序：加载 ST 程序、按扫描周期调度、与泵站过程仿真闭环，
// 并暴露 Web 监控台（实时变量/强制点/任务耗时与抖动）。
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"softplc/internal/plant"
	"softplc/internal/runtime"
	"softplc/internal/st"
	"softplc/internal/webui"
)

const scanTick = 1 * time.Second

type stdLog struct{}

func (stdLog) Logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func main() {
	var (
		addr    = flag.String("http", ":8080", "Web 监控台监听地址")
		prog    = flag.String("prog", "programs/pump_v1.st", "ST 程序文件")
		virtual = flag.Bool("virtual", true, "使用虚拟时钟（true=仿真确定性，false=墙钟）")
	)
	flag.Parse()

	cfg := runtime.Config{
		RootProgram: "CTRL",
		Log:         stdLog{},
		Tasks: []runtime.TaskConfig{
			{Name: "CTRL", POU: "CTRL", Period: scanTick, Priority: 20,
				Watchdog: 200 * time.Millisecond, MaxStmts: 2_000_000},
			{Name: "HOUSE", POU: "HOUSE", Period: 5 * time.Second, Priority: 10,
				Watchdog: 200 * time.Millisecond, MaxStmts: 2_000_000},
		},
		Points: []runtime.PointConfig{
			{Name: "LEVEL_M", Direction: runtime.DirInput, SafeValue: "0"},
			{Name: "PUMP1", Direction: runtime.DirOutput, SafeValue: "FALSE"},
			{Name: "PUMP2", Direction: runtime.DirOutput, SafeValue: "FALSE"},
			{Name: "VIN_CMD", Direction: runtime.DirOutput, SafeValue: "0"},
			{Name: "VOUT_CMD", Direction: runtime.DirOutput, SafeValue: "0"},
		},
	}

	var clk runtime.Clock
	if *virtual {
		clk = runtime.NewVirtualClock()
	} else {
		clk = runtime.NewRealClock()
	}
	ctl := runtime.New(cfg, clk)
	src, err := os.ReadFile(*prog)
	if err != nil {
		fatal("读取程序失败: %v", err)
	}
	if err := ctl.LoadProgram(map[string]string{*prog: string(src)}); err != nil {
		fatal("程序加载失败: %v", err)
	}

	stn := plant.New(plant.Config{
		Seed: 20261120, DT: scanTick.Seconds(),
		TankArea: 10.0, TankMin: 0.0, TankMax: 10.0,
		NoiseStd: 0.08, GlitchProb: 0.01, GlitchAmp: 0.6, StuckProb: 0.002,
	})

	// 扫描循环：拍前喂入过程输入，拍后把输出送给过程。
	go func() {
		for {
			_ = ctl.IO().SetPhysicalInput("LEVEL_M",
				st.RealVal(st.PrimitiveType("LREAL"), stn.TrueLevel()))
			if err := ctl.Tick(scanTick); err != nil {
				fmt.Fprintln(os.Stderr, "扫描错误:", err)
				time.Sleep(time.Second)
				continue
			}
			stn.Step(plant.Commands{
				Pump1On: ctl.IO().PhysicalOutput("PUMP1").AsBool(),
				Pump2On: ctl.IO().PhysicalOutput("PUMP2").AsBool(),
				InOpen:  ctl.IO().PhysicalOutput("VIN_CMD").AsReal(),
				OutOpen: ctl.IO().PhysicalOutput("VOUT_CMD").AsReal(),
			})
			if *virtual {
				// 虚拟时钟下不睡眠，全速推进仿真
				continue
			}
			time.Sleep(scanTick)
		}
	}()

	srv := webui.New(ctl)
	fmt.Fprintf(os.Stderr, "Web 监控台: http://localhost%s\n", *addr)
	if err := http.ListenAndServe(*addr, srv.Mux); err != nil {
		fatal("HTTP 服务失败: %v", err)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
