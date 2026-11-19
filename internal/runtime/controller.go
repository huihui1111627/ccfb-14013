package runtime

import (
	"fmt"
	"sort"
	"time"

	"softplc/internal/fb"
	"softplc/internal/st"
)

// LogSink 日志接口。
type LogSink interface {
	Logf(format string, args ...any)
}

type nopLog struct{}

func (nopLog) Logf(string, ...any) {}

// Config 控制器配置。
type Config struct {
	RootProgram string
	Tasks       []TaskConfig
	Points      []PointConfig
	Log         LogSink
}

// SwapRequest 在线换程序请求。
type SwapRequest struct {
	Files       map[string]string
	RootProgram string
	Points      []PointConfig // 新点位配置（可与旧的不同名）
}

// SwapReport 换程序迁移报告。
type SwapReport struct {
	Carried   []string // 同名同类型、值带过去的变量
	Converted []string // 类型变化、按规则转换的变量
	Reset     []string // 无法迁移、置初值/零值的变量
	New       []string // 新程序新增变量
	Dropped   []string // 旧程序有、新程序没有的变量
	FBState   []string // 功能块内部状态迁移情况
}

// Controller 软控制器。
type Controller struct {
	cfg   Config
	clock Clock
	io    *IO

	prog  *st.Program
	env   *st.ExecEnv
	gf    *st.Frame // 全局帧
	tasks []*Task

	swapRequested bool
	swapReq       *SwapRequest
	swapReport    *SwapReport
	watchdogTrip  bool
	tripTask      string
	// 本拍执行控制：已完成的任务不再触发抢占；调用栈防止递归重入
	tickDone  map[string]bool
	tickStack map[string]bool
	// 测试钩子：在执行到指定语句数时调用，用于模拟任务执行期间有新任务到点
	preemptHook func(running *Task)
}


// New 创建控制器（程序随后由 LoadProgram 装入）。
func New(cfg Config, clock Clock) *Controller {
	if cfg.Log == nil {
		cfg.Log = nopLog{}
	}
	return &Controller{
		cfg: cfg,
		clock: clock,
		io:  NewIO(),
	}
}

// LoadProgram 加载首版程序并完成全部帧实例化。
func (c *Controller) LoadProgram(files map[string]string) error {
	prog, err := st.Load(c.cfg.RootProgram, files)
	if err != nil {
		return err
	}
	c.prog = prog
	c.env = &st.ExecEnv{
		Prog:      prog,
		NewNative: fb.New,
	}
	c.gf = c.env.InstantiateGlobal()
	c.env.Root = c.gf
	if err := c.io.Bind(prog, c.cfg.Points); err != nil {
		return err
	}
	c.tasks = nil
	for _, tc := range c.cfg.Tasks {
		pou, ok := prog.Universe.POUs[tc.POU]
		if !ok {
			return fmt.Errorf("任务 %s 引用的 POU %s 不存在", tc.Name, tc.POU)
		}
		frame := c.env.Instantiate(pou, c.gf)
		t := &Task{
			Cfg:         tc,
			State:       TaskReady,
			frame:       frame,
			pou:         pou,
			nextDue:     0,
			jitter:      newJitter(),
		}
		if t.Cfg.MaxStmts == 0 {
			t.Cfg.MaxStmts = 10_000_000
		}
		c.tasks = append(c.tasks, t)
	}
	sort.SliceStable(c.tasks, func(i, j int) bool {
		return c.tasks[i].Cfg.Priority > c.tasks[j].Cfg.Priority
	})
	c.logf("程序 %s 加载完成：%d 个任务，%d 个 I/O 点", c.cfg.RootProgram, len(c.tasks), len(c.cfg.Points))
	return nil
}

// IO 暴露 I/O 管理器（仿真器、Web API 用）。
func (c *Controller) IO() *IO { return c.io }

// Clock 暴露时钟。
func (c *Controller) Clock() Clock { return c.clock }

func (c *Controller) logf(format string, args ...any) {
	c.cfg.Log.Logf("[softplc t=%s] %s", c.clock.Now(), fmt.Sprintf(format, args...))
}

// ---------- 扫描循环 ----------

// dueTasks 返回当前时刻到点的任务，按优先级从高到低。
func (c *Controller) dueTasks(now time.Duration) []*Task {
	var due []*Task
	for _, t := range c.tasks {
		if now >= t.nextDue {
			due = append(due, t)
		}
	}
	sort.SliceStable(due, func(i, j int) bool {
		return due[i].Cfg.Priority > due[j].Cfg.Priority
	})
	return due
}

// pendingHigher 是否有比 t 优先级更高且到点、本拍尚未完成且不在调用栈中的任务。
func (c *Controller) pendingHigher(t *Task, now time.Duration) *Task {
	for _, x := range c.tasks {
		if x == t {
			continue
		}
		if c.tickDone[x.Cfg.Name] || c.tickStack[x.Cfg.Name] {
			continue
		}
		if x.Cfg.Priority > t.Cfg.Priority && now >= x.nextDue {
			return x
		}
	}
	return nil
}

// Tick 推进一个基础扫描节拍：到点任务依次执行（高优先级可在语句边界抢占），
// 拍首锁存输入、拍末统一刷新输出。基础节拍取所有任务周期的最大公约数。
func (c *Controller) Tick(step time.Duration) error {
	now := c.clock.Now()
	c.tickDone = map[string]bool{}
	c.tickStack = map[string]bool{}
	// 拍首：锁存全部输入（强制点在锁存内处理）
	c.io.LatchInputs(c.gf)
	// 到点任务（已按优先级排序）；记录各任务本拍理想到期时刻
	due := c.dueTasks(now)
	for _, t := range due {
		if err := c.runTask(t, t.nextDue, now); err != nil {
			return err
		}
	}
	// 拍末：输出统一刷新；看门狗动作过则刷安全值
	if c.watchdogTrip {
		c.io.ApplySafety(c.gf)
	} else {
		c.io.FlushOutputs(c.gf)
	}
	// 推进虚拟时钟与任务到期时刻
	c.clock.Advance(step)
	for _, t := range c.tasks {
		if now >= t.nextDue {
			// 本拍已执行的任务推进到下一个到期点；错过的周期不累积（PLC 典型行为）
			for t.nextDue <= now {
				t.nextDue += t.Cfg.Period
			}
		}
	}
	// 两拍之间处理在线换程序
	if c.swapRequested {
		if err := c.applySwap(); err != nil {
			c.swapRequested = false
			return err
		}
	}
	return nil
}

// runTask 执行一个任务的一拍。抢占在 safeRun 内部以 panic 展开后处理，
// 统计（耗时/抖动/拍数）只在本方法最外层做一次。
// due=理想到期时刻，actual=实际执行时刻，用于唤醒抖动统计。
func (c *Controller) runTask(t *Task, due, actual time.Duration) error {
	if c.tickDone[t.Cfg.Name] {
		return nil // 本拍已完成（可能是作为抢占任务先跑完）
	}
	wallStart := time.Now()
	start := c.clock.Now()
	c.tickStack[t.Cfg.Name] = true
	t.State = TaskRunning
	cp := &Checkpoint{
		ctl:       c,
		task:      t,
		startWall: wallStart,
		budget:    t.Cfg.Watchdog,
		maxStmts:  t.Cfg.MaxStmts,
	}
	env := &st.ExecEnv{
		Prog:      c.prog,
		Root:      c.gf,
		CP:        cp,
		Now:       start,
		DT:        t.Cfg.Period,
		NewNative: fb.New,
	}
	execErr := c.safeRun(env, t)
	delete(c.tickStack, t.Cfg.Name)
	c.tickDone[t.Cfg.Name] = true
	elapsed := time.Since(wallStart)
	t.lastExec = elapsed
	t.totalExec += elapsed
	if elapsed > t.maxExec {
		t.maxExec = elapsed
	}
	t.runs++
	t.jitter.Sample(due, actual, elapsed)
	t.State = TaskReady
	return execErr
}

// safeRun 执行任务体并处理抢占 panic：高优先级任务在当前拍内嵌套执行完，
// 被抢占任务从检查点继续（检查点位于语句边界，已完成语句不回滚）。
func (c *Controller) safeRun(env *st.ExecEnv, t *Task) (err error) {
	defer func() {
		if r := recover(); r != nil {
			sig, ok := r.(*flowSignal)
			if !ok {
				panic(r)
			}
			switch sig.kind {
			case flowPreempt:
				// 触发抢占的高优先级任务立即执行（嵌套）
				hi := c.pendingHigher(t, c.clock.Now())
				if hi != nil {
					if e := c.runTask(hi, hi.nextDue, c.clock.Now()); e != nil {
						err = e
						return
					}
				}
				// 被抢占任务从断点继续：临时让出栈位，恢复后重新占位
				delete(c.tickStack, t.Cfg.Name)
				err = c.safeRun(env, t)
				c.tickStack[t.Cfg.Name] = true
			case flowWatchdog:
				// 看门狗已在 onWatchdog 处理状态，这里终止本拍
				err = nil
			case flowSwap:
				err = nil
			}
		}
	}()
	return env.Run(t.frame)
}

// onWatchdog 看门狗动作：标记跳闸、输出安全值。
func (c *Controller) onWatchdog(t *Task) {
	if !c.watchdogTrip {
		c.logf("看门狗动作：任务 %s 超时（预算 %s），全部输出退回安全值", t.Cfg.Name, t.Cfg.Watchdog)
	}
	c.watchdogTrip = true
	c.tripTask = t.Cfg.Name
}

// ResetWatchdog 人工复位看门狗（例如确认现场后）。
func (c *Controller) ResetWatchdog() {
	c.watchdogTrip = false
	c.tripTask = ""
	c.logf("看门狗已人工复位")
}

func (c *Controller) WatchdogTripped() bool { return c.watchdogTrip }

// ---------- 在线换程序 ----------

// RequestSwap 请求在下一拍边界换程序。
func (c *Controller) RequestSwap(req *SwapRequest) {
	c.swapRequested = true
	c.swapReq = req
}

// applySwap 在两拍之间执行原子换程序：加载新程序 -> 迁移变量 -> 替换帧。
func (c *Controller) applySwap() error {
	req := c.swapReq
	c.swapRequested = false
	c.swapReq = nil
	root := req.RootProgram
	if root == "" {
		root = c.cfg.RootProgram
	}
	newProg, err := st.Load(root, req.Files)
	if err != nil {
		c.logf("换程序失败（新程序加载检查未通过，继续运行旧程序）: %v", err)
		return fmt.Errorf("新程序加载失败，已保留旧程序: %w", err)
	}
	report := &SwapReport{}

	newEnv := &st.ExecEnv{Prog: newProg, NewNative: fb.New}
	newGF := newEnv.InstantiateGlobal()

	// 全局变量迁移（绑定一律取实例帧里的，布局绑定不含 FB 实例）
	for name, nb := range newGF.Vars {
		ob, ok := c.gf.Vars[name]
		if !ok {
			report.New = append(report.New, name)
			continue
		}
		if nb.IsFB || ob.IsFB {
			c.migrateFB(c.gf, ob, newGF, nb, name, report)
			continue
		}
		c.migrateScalar(c.gf, ob, newGF, nb, name, report)
	}
	for name := range c.gf.Vars {
		if _, ok := newGF.Vars[name]; !ok {
			report.Dropped = append(report.Dropped, name)
		}
	}
	// 任务帧迁移（按 POU 名匹配，保留局部变量）
	newTaskFrames := map[string]*st.Frame{}
	for _, tc := range c.cfg.Tasks {
		if pou, ok := newProg.Universe.POUs[tc.POU]; ok {
			nf := newEnv.Instantiate(pou, newGF)
			oldTask := c.findTask(tc.Name)
			if oldTask != nil {
				c.migrateFrame(oldTask.frame, nf, oldTask.pou, pou, report)
			}
			newTaskFrames[tc.Name] = nf
		}
	}
	// 提交替换
	c.prog = newProg
	c.env = newEnv
	c.gf = newGF
	c.env.Root = newGF
	points := req.Points
	if len(points) == 0 {
		points = c.cfg.Points
	}
	if err := c.io.Bind(newProg, points); err != nil {
		return err
	}
	for _, t := range c.tasks {
		if nf, ok := newTaskFrames[t.Cfg.Name]; ok {
			t.frame = nf
			t.pou = newProg.Universe.POUs[t.Cfg.POU]
			t.resumeLine = 0
		}
	}
	c.swapReport = report
	c.logf("在线换程序完成：带值 %d，转换 %d，重置 %d，新增 %d，移除 %d",
		len(report.Carried), len(report.Converted), len(report.Reset),
		len(report.New), len(report.Dropped))
	return nil
}

func (c *Controller) migrateScalar(oldFrame *st.Frame, ob *st.VarBinding,
	newFrame *st.Frame, nb *st.VarBinding, name string, report *SwapReport) {
	if ob.Type == nb.Type {
		c.copySameLayout(oldFrame, ob.Base, ob.Type, newFrame, nb.Base, nb.Type)
		report.Carried = append(report.Carried, name)
		return
	}
	// 类型变化：按规则转换标量叶子；聚合且元素同族时逐元素转换
	if ob.Type.Kind == nb.Type.Kind && (ob.Type.Kind == st.TArray || ob.Type.Kind == st.TStruct) {
		n := ob.Type.Size()
		if nb.Type.Size() < n {
			n = nb.Type.Size()
		}
		converted := 0
		for i := 0; i < n; i++ {
			ov := oldFrame.Slots[ob.Base+i]
			cv, err := st.Convert(ov, newFrame.Slots[nb.Base+i].Type)
			if err == nil {
				newFrame.Slots[nb.Base+i] = cv
				converted++
			}
		}
		if converted == n {
			report.Converted = append(report.Converted, name)
		} else {
			report.Reset = append(report.Reset, name)
		}
		return
	}
	// 标量类型转换
	ov := oldFrame.Slots[ob.Base]
	cv, err := st.Convert(ov, nb.Type)
	if err == nil {
		newFrame.Slots[nb.Base] = cv
		report.Converted = append(report.Converted, name)
	} else {
		report.Reset = append(report.Reset, name)
	}
}

func (c *Controller) copySameLayout(src *st.Frame, srcBase int, stType *st.Type,
	dst *st.Frame, dstBase int, _ *st.Type) {
	n := stType.Size()
	for i := 0; i < n; i++ {
		dst.Slots[dstBase+i] = src.Slots[srcBase+i]
	}
}

func (c *Controller) migrateFB(oldFrame *st.Frame, ob *st.VarBinding,
	newFrame *st.Frame, nb *st.VarBinding, name string, report *SwapReport) {
	// 原生 FB：快照/恢复内部状态（TON/TOF/TP/R_TRIG/F_TRIG）
	if ob.IsFB && nb.IsFB && ob.Type.Name == nb.Type.Name {
		oldInst := ob.FB
		newInst := nb.FB
		if oldInst == nil || newInst == nil {
			report.Reset = append(report.Reset, name+"(FB实例缺失)")
			return
		}
		if oldInst.Native != nil && newInst.Native != nil {
			newInst.Native.Restore(oldInst.Native.Snapshot())
			report.FBState = append(report.FBState, name+":"+ob.Type.Name+":状态保留")
			return
		}
		// 用户 FB：迁移实例帧槽位
		if oldInst.POU != nil && newInst.POU != nil {
			c.migrateFrame(oldInst, newInst, oldInst.POU, newInst.POU, report)
			report.FBState = append(report.FBState, name+":"+ob.Type.Name+":成员迁移")
			return
		}
	}
	report.Reset = append(report.Reset, name+"(FB)")
}

// migrateFrame 按同名变量迁移两个 POU 实例帧。
func (c *Controller) migrateFrame(oldF, newF *st.Frame, oldPOU, newPOU *st.POU, report *SwapReport) {
	_ = oldPOU
	_ = newPOU
	for name, nb := range newF.Vars {
		ob, ok := oldF.Vars[name]
		if !ok {
			continue
		}
		if nb.IsFB || ob.IsFB {
			c.migrateFB(oldF, ob, newF, nb, oldPOU.Name+"."+name, report)
			continue
		}
		c.migrateScalar(oldF, ob, newF, nb, oldPOU.Name+"."+name, report)
	}
}

func (c *Controller) findTask(name string) *Task {
	for _, t := range c.tasks {
		if t.Cfg.Name == name {
			return t
		}
	}
	return nil
}

// DebugArmTaskNow 把指定任务的到期时刻设为当前虚拟时刻，使其在当前正在执行的
// 任务的下一个检查点立即满足抢占条件。仅用于测试与调试。
func (c *Controller) DebugArmTaskNow(name string) bool {
	t := c.findTask(name)
	if t == nil {
		return false
	}
	t.nextDue = c.clock.Now()
	return true
}

// DebugSetNextDue 直接设置任务下一次到期时刻（测试用）。
func (c *Controller) DebugSetNextDue(name string, d time.Duration) bool {
	t := c.findTask(name)
	if t == nil {
		return false
	}
	t.nextDue = d
	return true
}

// SetPreemptHook 安装检查点钩子（测试用）。
func (c *Controller) SetPreemptHook(h func(running *Task)) { c.preemptHook = h }

// SwapReport 返回最近一次换程序报告。
func (c *Controller) SwapReport() *SwapReport { return c.swapReport }

// ---------- 观测接口 ----------

// TaskSnapshots 返回全部任务统计。
func (c *Controller) TaskSnapshots() []TaskSnapshot {
	out := make([]TaskSnapshot, 0, len(c.tasks))
	for _, t := range c.tasks {
		out = append(out, t.snapshot())
	}
	return out
}

// GlobalFrame 暴露全局帧（页面读变量）。
func (c *Controller) GlobalFrame() *st.Frame { return c.gf }

// Program 暴露当前程序。
func (c *Controller) Program() *st.Program { return c.prog }

// RunFor 连续跑虚拟时长（仿真/验收用）。每步取任务周期最大公约数。
func (c *Controller) RunFor(d time.Duration, hook func(now time.Duration)) error {
	step := c.baseStep()
	deadline := c.clock.Now() + d
	for c.clock.Now() < deadline {
		if err := c.Tick(step); err != nil {
			return err
		}
		if hook != nil {
			hook(c.clock.Now())
		}
	}
	return nil
}

// baseStep 取全部任务周期的最大公约数作为基础扫描节拍。
func (c *Controller) baseStep() time.Duration {
	if len(c.tasks) == 0 {
		return 10 * time.Millisecond
	}
	g := c.tasks[0].Cfg.Period
	for _, t := range c.tasks[1:] {
		g = gcdDur(g, t.Cfg.Period)
	}
	return g
}

func gcdDur(a, b time.Duration) time.Duration {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
