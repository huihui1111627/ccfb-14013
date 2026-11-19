package st_test

import (
	"testing"
	"time"

	"softplc/internal/fb"
	"softplc/internal/st"
)

func loadAndRun(t *testing.T, src string, ticks int, dt time.Duration) (*st.ExecEnv, *st.Frame) {
	t.Helper()
	prog, err := st.Load("MAIN", map[string]string{"test.st": src})
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	env := &st.ExecEnv{Prog: prog, NewNative: fb.New, Now: 0, DT: dt}
	gf := env.InstantiateGlobal()
	env.Root = gf
	rootFrame := env.Instantiate(prog.Root, gf)
	for i := 0; i < ticks; i++ {
		env.Now = time.Duration(i) * dt
		if err := env.Run(rootFrame); err != nil {
			t.Fatalf("第 %d 拍执行失败: %v", i, err)
		}
	}
	return env, rootFrame
}

func slot(gf *st.Frame, name string) st.Value {
	b := gf.Vars[name]
	return gf.Slots[b.Base]
}

// 1. 基本类型、结构体、数组、枚举、时间与位串字面量
func TestBasicTypesAndLiterals(t *testing.T) {
	src := `
TYPE
  Color : (RED, GREEN, BLUE);
  Tank : STRUCT
    level : INT;
    hi    : BOOL;
  END_STRUCT;
END_TYPE

VAR_GLOBAL
  GINT   : INT := 42;
  GDINT  : DINT := 16#FF;
  GWORD  : WORD := 2#1010_1100;
  GT     : TIME := T#2s500ms;
  GCOL   : Color := GREEN;
  GARR   : ARRAY[1..3, 0..1] OF INT;
  GTank  : Tank;
  GSTR   : STRING := 'hello$N';
END_VAR

PROGRAM MAIN
VAR
  i : INT;
END_VAR
BEGIN
END_PROGRAM
`
	prog, err := st.Load("MAIN", map[string]string{"test.st": src})
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	env := &st.ExecEnv{Prog: prog, NewNative: fb.New}
	gf := env.InstantiateGlobal()
	if got := slot(gf, "GINT").AsInt(); got != 42 {
		t.Errorf("GINT=%d 期望42", got)
	}
	if got := slot(gf, "GDINT").AsUint(); got != 255 {
		t.Errorf("GDINT=%d 期望255", got)
	}
	if got := slot(gf, "GWORD").AsUint(); got != 0xAC {
		t.Errorf("GWORD=%x 期望AC", got)
	}
	if got := time.Duration(slot(gf, "GT").V.(int64)); got != 2500*time.Millisecond {
		t.Errorf("GT=%s 期望2.5s", got)
	}
	if got := slot(gf, "GCOL").AsInt(); got != 1 {
		t.Errorf("GCOL=%d 期望1(GREEN)", got)
	}
	if got := slot(gf, "GSTR").V.(string); got != "hello\n" {
		t.Errorf("GSTR=%q 期望带换行", got)
	}
	// 数组布局：3*2=6 槽
	b := gf.Vars["GARR"]
	if b.Type.Size() != 6 {
		t.Errorf("数组 Size=%d 期望6", b.Type.Size())
	}
}

// 2. 加载时类型检查必须报错
func TestTypeCheckErrors(t *testing.T) {
	cases := map[string]string{
		"赋值类型不匹配": `
VAR_GLOBAL a : INT; b : BOOL; END_VAR
PROGRAM MAIN BEGIN a := b; END_PROGRAM`,
		"未声明变量": `
PROGRAM MAIN BEGIN x := 1; END_PROGRAM`,
		"数组越界维数": `
VAR_GLOBAL a : ARRAY[1..3] OF INT; END_VAR
PROGRAM MAIN BEGIN a[1,2] := 1; END_PROGRAM`,
		"IF 条件非 BOOL": `
VAR_GLOBAL a : INT; END_VAR
PROGRAM MAIN BEGIN IF a THEN a := 1; END_IF; END_PROGRAM`,
		"FOR 计数器非整数": `
VAR_GLOBAL r : REAL; END_VAR
PROGRAM MAIN BEGIN FOR r := 1 TO 3 DO END_FOR; END_PROGRAM`,
	}
	for name, src := range cases {
		if _, err := st.Load("MAIN", map[string]string{"t.st": src}); err == nil {
			t.Errorf("%s: 期望加载报错，实际通过", name)
		}
	}
}

// 3. TON 接通延时：3 拍后 Q 置位
func TestTONInProgram(t *testing.T) {
	src := `
VAR_GLOBAL
  INP   : BOOL;
  TQ    : BOOL;
  TET   : TIME;
END_VAR
PROGRAM MAIN
VAR
  timer : TON;
END_VAR
BEGIN
  timer(IN := INP, PT := T#30ms, Q => TQ, ET => TET);
END_PROGRAM
`
	prog, err := st.Load("MAIN", map[string]string{"t.st": src})
	if err != nil {
		t.Fatalf("加载: %v", err)
	}
	env := &st.ExecEnv{Prog: prog, NewNative: fb.New, DT: 10 * time.Millisecond}
	gf := env.InstantiateGlobal()
	env.Root = gf
	root := env.Instantiate(prog.Root, gf)
	inB := gf.Vars["INP"]
	qB := gf.Vars["TQ"]
	for i := 0; i < 5; i++ {
		gf.Slots[inB.Base] = st.BoolVal(true)
		env.Now = time.Duration(i) * 10 * time.Millisecond
		if err := env.Run(root); err != nil {
			t.Fatal(err)
		}
	}
	if !gf.Slots[qB.Base].AsBool() {
		t.Error("5 拍后 TON.Q 应为 TRUE")
	}
}

// 4. R_TRIG 只在上升沿那一拍为 TRUE
func TestRTrig(t *testing.T) {
	src := `
VAR_GLOBAL
  CLK : BOOL;
  Q   : BOOL;
END_VAR
PROGRAM MAIN
VAR
  edge : R_TRIG;
END_VAR
BEGIN
  edge(CLK := CLK, Q => Q);
END_PROGRAM
`
	prog, _ := st.Load("MAIN", map[string]string{"t.st": src})
	env := &st.ExecEnv{Prog: prog, NewNative: fb.New, DT: 10 * time.Millisecond}
	gf := env.InstantiateGlobal()
	env.Root = gf
	root := env.Instantiate(prog.Root, gf)
	CLKB := gf.Vars["CLK"]
	qB := gf.Vars["Q"]
	seq := []bool{false, true, true, false, true}
	qSeq := []bool{}
	for i, v := range seq {
		gf.Slots[CLKB.Base] = st.BoolVal(v)
		env.Now = time.Duration(i) * 10 * time.Millisecond
		if err := env.Run(root); err != nil {
			t.Fatal(err)
		}
		qSeq = append(qSeq, gf.Slots[qB.Base].AsBool())
	}
	want := []bool{false, true, false, false, true}
	for i := range want {
		if qSeq[i] != want[i] {
			t.Errorf("R_TRIG 第%d拍=%v 期望%v", i, qSeq[i], want[i])
		}
	}
}

// 5. 控制流、循环、CASE、数组下标、结构体字段、用户 FB、用户 FUNCTION
func TestControlFlowAndUserFB(t *testing.T) {
	src := `
TYPE
  Point : STRUCT
    x : INT;
    y : INT;
  END_STRUCT;
END_TYPE

FUNCTION Add : INT
VAR_INPUT
  a : INT;
  b : INT;
END_VAR
BEGIN
  Add := a + b;
END_FUNCTION

FUNCTION_BLOCK Accum
VAR_INPUT
  step : INT;
END_VAR
VAR_OUTPUT
  total : INT;
END_VAR
BEGIN
  total := Add(total, step);
END_FUNCTION_BLOCK

VAR_GLOBAL
  ARR  : ARRAY[0..4] OF INT;
  PT   : Point;
  SUM  : INT;
  MODE : INT;
  OUT  : INT;
END_VAR

PROGRAM MAIN
VAR
  acc : Accum;
  k   : INT;
  s   : INT;
END_VAR
BEGIN
  FOR k := 0 TO 4 DO
    ARR[k] := k * k;
  END_FOR;
  s := 0;
  WHILE s < 5 DO
    s := s + 1;
  END_WHILE;
  PT.x := ARR[3];
  PT.y := 7;
  SUM := 0;
  FOR k := 0 TO 4 DO
    SUM := Add(SUM, ARR[k]);
  END_FOR;
  acc(step := 2, total => OUT);
  CASE MODE OF
    0: OUT := OUT + 100;
    1..2: OUT := OUT + 200;
  ELSE
    OUT := OUT + 999;
  END_CASE;
  REPEAT
    MODE := MODE + 1;
  UNTIL MODE >= 3 END_REPEAT;
END_PROGRAM
`
	prog, err := st.Load("MAIN", map[string]string{"t.st": src})
	if err != nil {
		t.Fatalf("加载: %v", err)
	}
	env := &st.ExecEnv{Prog: prog, NewNative: fb.New, DT: 10 * time.Millisecond}
	gf := env.InstantiateGlobal()
	env.Root = gf
	root := env.Instantiate(prog.Root, gf)
	if err := env.Run(root); err != nil {
		t.Fatalf("执行: %v", err)
	}
	// ARR = 0,1,4,9,16 -> SUM=30
	if got := slot(gf, "SUM").AsInt(); got != 30 {
		t.Errorf("SUM=%d 期望30", got)
	}
	// 结构体字段槽：x 在 base，y 在 base+1
	PTB := gf.Vars["PT"]
	if gf.Slots[PTB.Base].AsInt() != 9 || gf.Slots[PTB.Base+1].AsInt() != 7 {
		t.Error("结构体字段赋值错误")
	}
	// acc.total=2，MODE 初始 0 -> CASE 0 -> OUT=102；REPEAT 后 MODE=3
	if got := slot(gf, "OUT").AsInt(); got != 102 {
		t.Errorf("OUT=%d 期望102", got)
	}
	if got := slot(gf, "MODE").AsInt(); got != 3 {
		t.Errorf("MODE=%d 期望3", got)
	}
	// 第二拍 FB 状态保持：total 再 +2 = 4，MODE=3 -> ELSE -> 999+4=1003
	if err := env.Run(root); err != nil {
		t.Fatal(err)
	}
	if got := slot(gf, "OUT").AsInt(); got != 1003 {
		t.Errorf("第二拍 OUT=%d 期望1003（FB 保持+CASE ELSE）", got)
	}
}

// 6. 数组下标越界要在运行期报错
func TestArrayBoundsRuntime(t *testing.T) {
	src := `
VAR_GLOBAL a : ARRAY[1..3] OF INT; END_VAR
PROGRAM MAIN BEGIN a[5] := 1; END_PROGRAM
`
	prog, err := st.Load("MAIN", map[string]string{"t.st": src})
	if err != nil {
		t.Fatalf("加载: %v", err)
	}
	env := &st.ExecEnv{Prog: prog, NewNative: fb.New, DT: 10 * time.Millisecond}
	gf := env.InstantiateGlobal()
	env.Root = gf
	root := env.Instantiate(prog.Root, gf)
	if err := env.Run(root); err == nil {
		t.Error("数组越界应报运行时错误")
	}
}
