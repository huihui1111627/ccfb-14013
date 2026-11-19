// Package webui 提供软控制器的监控与操作 Web 界面：
// 实时变量、强制点（醒目标记）、各任务每拍耗时与抖动、在线换程序。
package webui

import (
	"embed"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"softplc/internal/runtime"
	"softplc/internal/st"
)

//go:embed static/*
var staticFS embed.FS

// Server 包装控制器对外提供 HTTP 接口。
type Server struct {
	Ctl *runtime.Controller
	Mux *http.ServeMux
}

func New(ctl *runtime.Controller) *Server {
	s := &Server{Ctl: ctl, Mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.Mux.HandleFunc("/api/vars", s.handleVars)
	s.Mux.HandleFunc("/api/tasks", s.handleTasks)
	s.Mux.HandleFunc("/api/force", s.handleForce)
	s.Mux.HandleFunc("/api/unforce", s.handleUnforce)
	s.Mux.HandleFunc("/api/status", s.handleStatus)
	s.Mux.Handle("/", http.FileServer(http.FS(staticFS)))
}

// VarView 变量视图。
type VarView struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Value  string `json:"value"`
	Forced bool   `json:"forced"`
	IsIO   bool   `json:"isIO"`
	Kind   string `json:"kind"`
}

func (s *Server) handleVars(w http.ResponseWriter, r *http.Request) {
	f := s.Ctl.GlobalFrame()
	io := s.Ctl.IO()
	ioPoints := map[string]*runtime.Point{}
	for _, p := range io.Points() {
		ioPoints[p.Cfg.Name] = p
	}
	var views []VarView
	for name, b := range f.Vars {
		vv := VarView{Name: name, Type: b.Type.Name}
		if p, ok := ioPoints[name]; ok {
			vv.IsIO = true
			vv.Forced = p.Forced
			vv.Kind = p.Cfg.Direction.String()
		}
		if b.IsFB {
			vv.Value = "<FB " + b.Type.Name + ">"
			vv.Kind = "FB"
		} else if b.Type.Kind == st.TArray || b.Type.Kind == st.TStruct {
			vv.Value = aggregatePreview(f, b)
			vv.Kind = typeKindName(b.Type.Kind)
		} else {
			vv.Value = st.FormatValue(f.Slots[b.Base])
			if vv.Kind == "" {
				vv.Kind = typeKindName(b.Type.Kind)
			}
		}
		views = append(views, vv)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	writeJSON(w, views)
}

func aggregatePreview(f *st.Frame, b *st.VarBinding) string {
	n := b.Type.Size()
	if n > 8 {
		n = 8
	}
	out := "["
	for i := 0; i < n; i++ {
		if i > 0 {
			out += ","
		}
		out += st.FormatValue(f.Slots[b.Base+i])
	}
	if b.Type.Size() > 8 {
		out += ",..."
	}
	return out + "]"
}

func typeKindName(k st.TypeKind) string {
	switch k {
	case st.TBool:
		return "BOOL"
	case st.TInt, st.TUint:
		return "INT"
	case st.TReal:
		return "REAL"
	case st.TString:
		return "STRING"
	case st.TTime:
		return "TIME"
	case st.TEnum:
		return "ENUM"
	case st.TStruct:
		return "STRUCT"
	case st.TArray:
		return "ARRAY"
	case st.TFuncBlock:
		return "FB"
	}
	return "?"
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	snaps := s.Ctl.TaskSnapshots()
	type taskJSON struct {
		runtime.TaskSnapshot
		Period     string `json:"periodText"`
		LastExec   string `json:"lastExecText"`
		MaxExec    string `json:"maxExecText"`
		MeanExec   string `json:"meanExecText"`
		JitterMin  string `json:"jitterMinText"`
		JitterMax  string `json:"jitterMaxText"`
		JitterMean string `json:"jitterMeanText"`
		JitterStd  string `json:"jitterStdText"`
	}
	out := make([]taskJSON, 0, len(snaps))
	for _, t := range snaps {
		out = append(out, taskJSON{
			TaskSnapshot: t,
			Period:       t.Period.String(),
			LastExec:     t.LastExec.String(),
			MaxExec:      t.MaxExec.String(),
			MeanExec:     t.MeanExec.String(),
			JitterMin:    t.JitterMin.String(),
			JitterMax:    t.JitterMax.String(),
			JitterMean:   t.JitterMean.String(),
			JitterStd:    t.JitterStd.String(),
		})
	}
	writeJSON(w, out)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"now":              s.Ctl.Clock().Now().String(),
		"watchdogTripped":  s.Ctl.WatchdogTripped(),
		"forcedCount":      len(s.Ctl.IO().ForcedPoints()),
		"swapAvailable":    true,
	})
}

type forceReq struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (s *Server) handleForce(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req forceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.Ctl.IO().Force(req.Name, req.Value); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"status": "forced", "name": req.Name})
}

func (s *Server) handleUnforce(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req forceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.Ctl.IO().Unforce(req.Name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"status": "unforced", "name": req.Name})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// 防止 time 包未使用告警（状态接口扩展时使用）。
var _ = time.Second
