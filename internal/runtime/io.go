package runtime

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"softplc/internal/st"
)

// Direction 点位方向。
type Direction int

const (
	DirInput Direction = iota
	DirOutput
)

func (d Direction) String() string {
	if d == DirInput {
		return "INPUT"
	}
	return "OUTPUT"
}

// PointConfig 是一个 I/O 点的静态配置。
type PointConfig struct {
	Name      string    // 唯一点位名，同时是全局变量名（标量叶子）
	Direction Direction
	SafeValue string    // 看门狗动作后输出点退回的安全值（ST 字面量写法）
	Forced    bool      // 启动即强制（可选）
	ForceVal  string    // 启动强制值
}

// Point 是运行期 I/O 点。
type Point struct {
	Cfg      PointConfig
	Type     *st.Type
	Slot     int // 全局帧槽位
	Forced   bool
	ForceVal st.Value
	SafeVal  st.Value
}

// IO 管理全部 I/O 点、过程映像与强制状态。
type IO struct {
	points map[string]*Point
	order  []string
	// 输入过程映像：每拍开始时从输入点锁存一份，程序只读这份
	inputImage map[string]st.Value
	// 输出过程映像：程序写全局帧，拍末统一提交到物理输出（这里是仿真接口）
	outputImage map[string]st.Value
	// 外部物理输入（仿真器写入），锁存前的实时值
	physicalIn map[string]st.Value
	// 外部物理输出（拍末刷新写入），仿真器读取
	physicalOut map[string]st.Value
}

func NewIO() *IO {
	return &IO{
		points:      map[string]*Point{},
		inputImage:  map[string]st.Value{},
		outputImage: map[string]st.Value{},
		physicalIn:  map[string]st.Value{},
		physicalOut: map[string]st.Value{},
	}
}

// Bind 按程序绑定全部点位（加载/换程序后调用）。
func (io *IO) Bind(prog *st.Program, cfgs []PointConfig) error {
	layout := prog.GlobalLayout
	newPoints := map[string]*Point{}
	newOrder := make([]string, 0, len(cfgs))
	for _, cfg := range cfgs {
		b, ok := layout.Vars[cfg.Name]
		if !ok {
			return fmt.Errorf("I/O 点 %s 在程序中没有对应的全局变量", cfg.Name)
		}
		if b.IsFB || b.Type.Kind == st.TArray || b.Type.Kind == st.TStruct {
			return fmt.Errorf("I/O 点 %s 必须是标量全局变量", cfg.Name)
		}
		safe, err := parseLiteral(cfg.SafeValue, b.Type)
		if err != nil {
			return fmt.Errorf("I/O 点 %s 安全值无效: %w", cfg.Name, err)
		}
		p := &Point{Cfg: cfg, Type: b.Type, Slot: b.Base, SafeVal: safe}
		// 换程序后保留强制状态（值按新类型重新解析）
		if old, existed := io.points[cfg.Name]; existed && old.Forced {
			fv, err := parseLiteral(formatForStorage(old.ForceVal), b.Type)
			if err != nil {
				return fmt.Errorf("I/O 点 %s 强制值无法迁移到新类型: %w", cfg.Name, err)
			}
			p.Forced = true
			p.ForceVal = fv
		} else if cfg.Forced {
			fv, err := parseLiteral(cfg.ForceVal, b.Type)
			if err != nil {
				return fmt.Errorf("I/O 点 %s 强制值无效: %w", cfg.Name, err)
			}
			p.Forced = true
			p.ForceVal = fv
		}
		newPoints[cfg.Name] = p
		newOrder = append(newOrder, cfg.Name)
		// 物理映像保留历史值（换程序不丢过程状态）
		if _, ok := io.physicalIn[cfg.Name]; !ok {
			io.physicalIn[cfg.Name] = st.ZeroValue(b.Type)
		}
		if _, ok := io.physicalOut[cfg.Name]; !ok {
			io.physicalOut[cfg.Name] = st.ZeroValue(b.Type)
		}
	}
	io.points = newPoints
	io.order = newOrder
	return nil
}

func (io *IO) Points() []*Point {
	out := make([]*Point, 0, len(io.order))
	for _, n := range io.order {
		out = append(out, io.points[n])
	}
	return out
}

func (io *IO) Get(name string) *Point { return io.points[name] }

// SetPhysicalInput 仿真器/驱动写物理输入。
func (io *IO) SetPhysicalInput(name string, v st.Value) error {
	if _, ok := io.points[name]; !ok {
		return fmt.Errorf("未知输入点 %s", name)
	}
	io.physicalIn[name] = v
	return nil
}

// PhysicalOutput 取拍末刷新后的物理输出。
func (io *IO) PhysicalOutput(name string) st.Value {
	return io.physicalOut[name]
}

// LatchInputs 拍首锁存：把当前物理输入（含强制覆盖）复制成输入映像，
// 并写入全局帧。一拍之内程序看到的输入不再变化。
func (io *IO) LatchInputs(frame *st.Frame) {
	for _, name := range io.order {
		p := io.points[name]
		if p.Cfg.Direction != DirInput {
			continue
		}
		v := io.physicalIn[name]
		if p.Forced {
			v = p.ForceVal
		}
		cv, err := st.Convert(v, p.Type)
		if err != nil {
			cv = st.ZeroValue(p.Type)
		}
		io.inputImage[name] = cv
		frame.Slots[p.Slot] = cv
	}
}

// FlushOutputs 拍末统一刷新：全局帧 -> 输出映像 -> 物理输出；
// 强制中的点始终输出强制值。
func (io *IO) FlushOutputs(frame *st.Frame) {
	for _, name := range io.order {
		p := io.points[name]
		if p.Cfg.Direction != DirOutput {
			continue
		}
		v := frame.Slots[p.Slot]
		if p.Forced {
			v = p.ForceVal
		}
		io.outputImage[name] = v
		io.physicalOut[name] = v
	}
}

// ApplySafety 看门狗动作：所有输出点退回各自配置的安全值（强制点仍保持强制）。
func (io *IO) ApplySafety(frame *st.Frame) {
	for _, name := range io.order {
		p := io.points[name]
		if p.Cfg.Direction != DirOutput {
			continue
		}
		v := p.SafeVal
		if p.Forced {
			v = p.ForceVal
		}
		frame.Slots[p.Slot] = v
		io.outputImage[name] = v
		io.physicalOut[name] = v
	}
}

// Force 人工强制一个点。
func (io *IO) Force(name, literal string) error {
	p, ok := io.points[name]
	if !ok {
		return fmt.Errorf("未知点位 %s", name)
	}
	v, err := parseLiteral(literal, p.Type)
	if err != nil {
		return err
	}
	p.Forced = true
	p.ForceVal = v
	// 输出点立即生效到物理侧；输入点不污染物理输入，
	// 只在下一拍 LatchInputs 时以强制值覆盖过程映像（解除后自然恢复物理值）。
	if p.Cfg.Direction == DirInput {
		io.inputImage[name] = v
	} else {
		io.physicalOut[name] = v
		io.outputImage[name] = v
	}
	return nil
}

// Unforce 解除强制。
func (io *IO) Unforce(name string) error {
	p, ok := io.points[name]
	if !ok {
		return fmt.Errorf("未知点位 %s", name)
	}
	p.Forced = false
	return nil
}

// ForcedPoints 返回强制中点位（页面醒目标记用）。
func (io *IO) ForcedPoints() []*Point {
	var out []*Point
	for _, name := range io.order {
		if io.points[name].Forced {
			out = append(out, io.points[name])
		}
	}
	return out
}

// parseLiteral 按目标类型解析 ST 字面量（强制值/安全值配置用）。
func parseLiteral(s string, dst *st.Type) (st.Value, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return st.ZeroValue(dst), nil
	}
	up := strings.ToUpper(s)
	switch dst.Kind {
	case st.TBool:
		if up == "TRUE" || up == "1" {
			return st.BoolVal(true), nil
		}
		if up == "FALSE" || up == "0" {
			return st.BoolVal(false), nil
		}
		return st.Value{}, fmt.Errorf("BOOL 只接受 TRUE/FALSE: %s", s)
	case st.TInt, st.TEnum:
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return st.Value{}, err
		}
		return st.Convert(st.IntVal(st.PrimitiveType("LINT"), v), dst)
	case st.TUint:
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return st.Value{}, err
		}
		return st.Convert(st.UintVal(st.PrimitiveType("LWORD"), v), dst)
	case st.TReal:
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return st.Value{}, err
		}
		return st.RealVal(dst, v), nil
	case st.TString:
		return st.StrVal(strings.Trim(s, "'")), nil
	case st.TTime:
		d, err := st.ParseTimeLiteral(up)
		if err != nil {
			return st.Value{}, err
		}
		return st.Value{Type: dst, V: int64(d)}, nil
	}
	return st.Value{}, fmt.Errorf("不支持的字面量类型 %s", dst.Name)
}

// formatForStorage 把旧值转回 parseLiteral 能解析的文本。
func formatForStorage(v st.Value) string {
	switch v.Type.Kind {
	case st.TBool:
		if v.AsBool() {
			return "TRUE"
		}
		return "FALSE"
	case st.TReal:
		return strconv.FormatFloat(v.AsReal(), 'g', -1, 64)
	case st.TTime:
		return (time.Duration(v.V.(int64))).String()
	case st.TString:
		return "'" + v.V.(string) + "'"
	default:
		return strconv.FormatInt(v.AsInt(), 10)
	}
}
