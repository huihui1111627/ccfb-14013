// Package plant 是泵站被控对象的过程仿真：
// 水池液位积分、泵启停延迟与流量爬升、阀门行程时间、传感器噪声与毛刺。
// 全部时间用虚拟纳秒，确定性伪随机（给定种子可完全复现）。
package plant

import (
	"math"
)

// rng 是确定性 xorshift64 伪随机源，避免依赖全局 rand。
type rng struct {
	state uint64
}

func newRNG(seed uint64) *rng {
	if seed == 0 {
		seed = 0x9E3779B97F4A7C15
	}
	return &rng{state: seed}
}

func (r *rng) next() uint64 {
	x := r.state
	x ^= x << 13
	x ^= x >> 7
	x ^= x << 17
	r.state = x
	return x
}

// float64 返回 [0,1)
func (r *rng) float64() float64 {
	return float64(r.next()>>11) / float64(1<<53)
}

// Pump 模型一台离心泵：命令到实际运转有启停延迟，流量随转速爬升。
type Pump struct {
	Name        string
	StartDelay  float64 // 秒：命令启动到开始转动
	StopDelay   float64 // 秒：命令停止到完全停转
	RampTime    float64 // 秒：从启动到达到额定流量
	 RatedFlow  float64 // m3/s 额定流量
	cmdOn       bool
	runTimer    float64 // 已运转/启停过程计时（秒）
	starting    bool
	running     bool
	speed       float64 // 0..1
	startEvents int
	stopEvents  int
}

// State 返回泵当前是否在实际运转（用于频繁启停统计）。
func (p *Pump) IsRunning() bool { return p.running || p.starting }

// Speed 当前转速比例。
func (p *Pump) Speed() float64 { return p.speed }

// SetCommand 设置启停命令。
func (p *Pump) SetCommand(on bool) {
	if on == p.cmdOn {
		return
	}
	p.cmdOn = on
	if on {
		p.starting = true
		p.startEvents++
		p.runTimer = 0
	} else {
		p.stopEvents++
	}
}

// update 推进 dt 秒，返回当前实际流量 m3/s。
func (p *Pump) update(dt float64) float64 {
	if p.cmdOn {
		if p.starting {
			p.runTimer += dt
			if p.runTimer >= p.StartDelay {
				p.starting = false
				p.running = true
			}
		}
		if p.running {
			// 转速爬升
			if p.speed < 1 {
				p.speed = math.Min(1, p.speed+dt/p.RampTime)
			}
		}
	} else {
		// 停机：停转延迟内维持，随后转速下降
		p.runTimer += dt
		if p.runTimer >= p.StopDelay {
			p.running = false
			p.starting = false
			if p.speed > 0 {
				p.speed = math.Max(0, p.speed-dt/p.RampTime)
			}
		}
	}
	return p.speed * p.RatedFlow
}

// Valve 模型一个电动调节阀：从当前位置到目标位置需要行程时间。
type Valve struct {
	Name       string
	TravelTime float64 // 秒：全关到全开
	position   float64 // 0..1 实际位置
	target     float64 // 0..1 目标位置
	coef       float64 // 流量系数（相对）
}

// SetTarget 设置目标开度 0..1。
func (v *Valve) SetTarget(x float64) {
	v.target = math.Max(0, math.Min(1, x))
}

// Position 当前实际开度。
func (v *Valve) Position() float64 { return v.position }

func (v *Valve) update(dt float64) {
	if v.position == v.target {
		return
	}
	step := dt / v.TravelTime
	if v.position < v.target {
		v.position = math.Min(v.target, v.position+step)
	} else {
		v.position = math.Max(v.target, v.position-step)
	}
}

// Tank 模型一个敞口水池：液位 = 体积/截面积，带进出流积分。
type Tank struct {
	Area       float64 // m2 横截面积
	Level      float64 // m 当前液位
	MinLevel   float64
	MaxLevel   float64
	Inflow     float64 // m3/s 当前实际入流
	Outflow    float64 // m3/s 当前实际出流
}

// update 推进 dt 秒，积分液位并做物理限幅（溢流/抽空）。
func (t *Tank) update(dt float64) {
	net := (t.Inflow - t.Outflow) / t.Area
	t.Level += net * dt
	if t.Level > t.MaxLevel {
		t.Level = t.MaxLevel // 溢流
	}
	if t.Level < t.MinLevel {
		t.Level = t.MinLevel // 抽空
	}
}

// Sensor 带噪声与毛刺的液位传感器。
type Sensor struct {
	NoiseStd   float64 // 高斯噪声标准差（m）
	GlitchProb float64 // 每拍出现毛刺的概率
	GlitchAmp  float64 // 毛刺幅度（m）
	StuckProb  float64 // 每拍卡死的概率（持续若干拍）
	rng        *rng
	stuckLeft  int
	lastVal    float64
}

func newSensor(rng *rng, noise, glitchP, glitchAmp, stuckP float64) *Sensor {
	return &Sensor{
		rng: rng, NoiseStd: noise, GlitchProb: glitchP,
		GlitchAmp: glitchAmp, StuckProb: stuckP,
	}
}

// gaussian 用 Box-Muller 产生标准正态噪声。
func (s *Sensor) gaussian() float64 {
	u1 := s.rng.float64()
	u2 := s.rng.float64()
	if u1 < 1e-12 {
		u1 = 1e-12
	}
	return math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u2)
}

// Read 给出当前拍的传感器读数（含噪声/毛刺/卡死）。
func (s *Sensor) Read(trueLevel float64) float64 {
	if s.stuckLeft > 0 {
		s.stuckLeft--
		return s.lastVal
	}
	if s.rng.float64() < s.StuckProb {
		s.stuckLeft = 2 + int(s.rng.float64()*4) // 卡死 2~5 拍
		s.lastVal = trueLevel
		return s.lastVal
	}
	v := trueLevel + s.gaussian()*s.NoiseStd
	if s.rng.float64() < s.GlitchProb {
		sign := 1.0
		if s.rng.float64() < 0.5 {
			sign = -1
		}
		v += sign * s.GlitchAmp
	}
	s.lastVal = v
	return v
}

// Station 组装整个泵站过程对象。
type Station struct {
	rng    *rng
	Tank   *Tank
	Pumps  []*Pump
	InValve *Valve
	OutValve *Valve
	Sensor *Sensor
	// 外部来水扰动（m3/s），由场景设定
	SourceFlow float64
	dt         float64 // 秒/拍
}

// Config 仿真对象参数。
type Config struct {
	Seed              uint64
	DT                float64
	TankArea          float64
	TankMin, TankMax  float64
	NoiseStd          float64
	GlitchProb        float64
	GlitchAmp         float64
	StuckProb         float64
}

// New 建一个双泵泵站。
func New(cfg Config) *Station {
	r := newRNG(cfg.Seed)
	tank := &Tank{
		Area: cfg.TankArea, MinLevel: cfg.TankMin, MaxLevel: cfg.TankMax,
		Level: (cfg.TankMin + cfg.TankMax) / 2,
	}
	pumps := []*Pump{
		{Name: "P1", StartDelay: 2.0, StopDelay: 1.0, RampTime: 3.0, RatedFlow: 0.9},
		{Name: "P2", StartDelay: 2.5, StopDelay: 1.0, RampTime: 3.5, RatedFlow: 1.1},
	}
	inV := &Valve{Name: "VIN", TravelTime: 8.0, coef: 1.0}
	outV := &Valve{Name: "VOUT", TravelTime: 6.0, coef: 1.0}
	sensor := newSensor(r, cfg.NoiseStd, cfg.GlitchProb, cfg.GlitchAmp, cfg.StuckProb)
	return &Station{
		rng: r, Tank: tank, Pumps: pumps,
		InValve: inV, OutValve: outV, Sensor: sensor,
		SourceFlow: 1.2, dt: cfg.DT,
	}
}

// Commands 是控制器每拍写给过程的命令。
type Commands struct {
	Pump1On bool
	Pump2On bool
	InOpen  float64 // 目标开度 0..1
	OutOpen float64
}

// Step 推进一拍物理过程，返回传感器读数。
func (s *Station) Step(cmd Commands) float64 {
	// 命令下发
	s.Pumps[0].SetCommand(cmd.Pump1On)
	s.Pumps[1].SetCommand(cmd.Pump2On)
	s.InValve.SetTarget(cmd.InOpen)
	s.OutValve.SetTarget(cmd.OutOpen)
	// 阀门/泵推进
	s.InValve.update(s.dt)
	s.OutValve.update(s.dt)
	pumpFlow := 0.0
	for _, p := range s.Pumps {
		pumpFlow += p.update(s.dt)
	}
	// 入流：来水 × 进水阀开度；出流：泵流量 × 出水阀开度
	s.Tank.Inflow = s.SourceFlow * s.InValve.Position()
	s.Tank.Outflow = pumpFlow * s.OutValve.Position()
	s.Tank.update(s.dt)
	return s.Sensor.Read(s.Tank.Level)
}

// TrueLevel 返回无噪声真实液位（验收判据用）。
func (s *Station) TrueLevel() float64 { return s.Tank.Level }

// PumpStartCount 累计泵启动次数（频繁启停判据）。
func (s *Station) PumpStartCount() int {
	n := 0
	for _, p := range s.Pumps {
		n += p.startEvents
	}
	return n
}

// PumpStates 返回各泵实际运转状态。
func (s *Station) PumpStates() []bool {
	out := make([]bool, len(s.Pumps))
	for i, p := range s.Pumps {
		out[i] = p.IsRunning()
	}
	return out
}
