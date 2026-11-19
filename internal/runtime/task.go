package runtime

import (
	"fmt"
	"time"

	"softplc/internal/st"
)

// 控制流信号：解释器遇到检查点时以 panic 方式展开，调度器按类型恢复。
type flowSignal struct {
	kind flowKind
}

type flowKind int

const (
	flowPreempt flowKind = iota // 被高优先级任务抢占
	flowWatchdog                // 看门狗超时
	flowSwap                    // 两拍之间在线换程序挂起
)

func (f *flowSignal) Error() string {
	switch f.kind {
	case flowPreempt:
		return "__preempt__"
	case flowWatchdog:
		return "__watchdog__"
	default:
		return "__swap__"
	}
}

// TaskState 任务状态。
type TaskState int

const (
	TaskReady TaskState = iota
	TaskRunning
	TaskSuspended // 被抢占挂起，保留断点
	TaskDone
)

// TaskConfig 任务静态配置。
type TaskConfig struct {
	Name        string
	POU         string // 关联的 POU 名
	Period      time.Duration
	Priority    int // 数字越大优先级越高
	Watchdog    time.Duration
	MaxStmts    int // 单拍语句执行数上限（看门狗的确定性口径，0=用默认值）
}

// Task 是运行期任务。
type Task struct {
	Cfg        TaskConfig
	State      TaskState
	frame      *st.Frame
	pou        *st.POU
	nextDue    time.Duration
	resumeLine int
	// 执行耗时统计（墙钟口径）
	lastExec  time.Duration
	maxExec   time.Duration
	totalExec time.Duration
	runs      int64
	deadlines int64
	jitter    *Jitter
}

// Checkpoint 实现 st.Checkpointer，在每条语句边界被解释器调用。
type Checkpoint struct {
	ctl       *Controller
	task      *Task
	startWall time.Time     // 本拍墙钟起点
	budget    time.Duration // 墙钟看门狗预算
	maxStmts  int
	stmts     int
}

// Checkpoint 检查：看门狗超时（墙钟 + 语句数双口径）、抢占、换程序。
func (cp *Checkpoint) Checkpoint(line int) error {
	cp.stmts++
	if cp.ctl.preemptHook != nil && cp.stmts == 50 {
		cp.ctl.preemptHook(cp.task)
	}
	// 1. 看门狗：墙钟预算（真实死循环也能抓住）
	if time.Since(cp.startWall) >= cp.budget {
		cp.task.deadlines++
		cp.ctl.onWatchdog(cp.task)
		panic(&flowSignal{kind: flowWatchdog})
	}
	// 2. 看门狗：单拍语句数上限（虚拟时钟下的确定性口径，无墙钟依赖）
	if cp.maxStmts > 0 && cp.stmts >= cp.maxStmts {
		cp.task.deadlines++
		cp.ctl.onWatchdog(cp.task)
		panic(&flowSignal{kind: flowWatchdog})
	}
	// 3. 高优先级任务到点：抢占
	if hi := cp.ctl.pendingHigher(cp.task, cp.ctl.clock.Now()); hi != nil {
		cp.task.State = TaskSuspended
		cp.task.resumeLine = line
		cp.ctl.logf("任务 %s 被 %s 抢占于第 %d 行", cp.task.Cfg.Name, hi.Cfg.Name, line)
		panic(&flowSignal{kind: flowPreempt})
	}
	// 4. 挂起的换程序请求（双保险，正常在两拍之间处理）
	if cp.ctl.swapRequested {
		panic(&flowSignal{kind: flowSwap})
	}
	return nil
}

var _ st.Checkpointer = (*Checkpoint)(nil)

// TaskSnapshot 是页面/API 看到的任务统计快照。
type TaskSnapshot struct {
	Name        string
	Period      time.Duration
	Priority    int
	Watchdog    time.Duration
	State       string
	LastExec    time.Duration
	MaxExec     time.Duration
	MeanExec    time.Duration
	Runs        int64
	Deadlines   int64
	ResumeLine  int
	// 唤醒抖动（实际执行时刻 - 理想到期时刻）
	JitterMin   time.Duration
	JitterMax   time.Duration
	JitterMean  time.Duration
	JitterStd   time.Duration
	JitterCount int64
}

func (t *Task) snapshot() TaskSnapshot {
	var mean time.Duration
	if t.runs > 0 {
		mean = t.totalExec / time.Duration(t.runs)
	}
	snap := TaskSnapshot{
		Name:       t.Cfg.Name,
		Period:     t.Cfg.Period,
		Priority:   t.Cfg.Priority,
		Watchdog:   t.Cfg.Watchdog,
		State:      stateName(t.State),
		LastExec:   t.lastExec,
		MaxExec:    t.maxExec,
		MeanExec:   mean,
		Runs:       t.runs,
		Deadlines:  t.deadlines,
		ResumeLine: t.resumeLine,
	}
	if t.jitter != nil {
		snap.JitterMin = t.jitter.MinRel
		snap.JitterMax = t.jitter.MaxRel
		snap.JitterMean = t.jitter.MeanRel
		snap.JitterStd = t.jitter.StdRel
		snap.JitterCount = t.jitter.Count
	}
	return snap
}

func stateName(s TaskState) string {
	switch s {
	case TaskReady:
		return "READY"
	case TaskRunning:
		return "RUNNING"
	case TaskSuspended:
		return "SUSPENDED"
	case TaskDone:
		return "DONE"
	}
	return "?"
}

func (s TaskSnapshot) String() string {
	return fmt.Sprintf("%s prio=%d period=%s last=%s max=%s runs=%d wd=%d",
		s.Name, s.Priority, s.Period, s.LastExec, s.MaxExec, s.Runs, s.Deadlines)
}
