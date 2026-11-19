package runtime

import (
	"math"
	"time"
)

// Jitter 周期抖动统计。
// 两个口径：
//  1. 唤醒抖动（release jitter）：任务实际执行时刻与理想到期时刻的偏差，
//     这是 PLC 扫描周期抖动的标准定义，真实/虚拟时钟都适用；
//  2. 执行耗时：单拍墙钟执行耗时，反映负载。
type Jitter struct {
	Count int64
	// 唤醒偏差（实际 - 理想），理想为 0
	MinRel  time.Duration
	MaxRel  time.Duration
	MeanRel time.Duration
	StdRel  time.Duration
	// 执行耗时
	MinCost  time.Duration
	MaxCost  time.Duration
	MeanCost time.Duration

	meanRelF  float64
	sumSqRel  float64
	meanCostF float64
}

func newJitter() *Jitter {
	return &Jitter{
		MinRel:  time.Duration(math.MaxInt64),
		MinCost: time.Duration(math.MaxInt64),
	}
}

// Sample 记录一次任务执行。
//   - due：该拍的理想到期虚拟时刻；actual：实际执行的虚拟时刻
//   - cost：本拍墙钟执行耗时
func (j *Jitter) Sample(due, actual, cost time.Duration) {
	rel := actual - due
	j.Count++
	n := float64(j.Count)
	if rel < j.MinRel {
		j.MinRel = rel
	}
	if rel > j.MaxRel {
		j.MaxRel = rel
	}
	if cost < j.MinCost {
		j.MinCost = cost
	}
	if cost > j.MaxCost {
		j.MaxCost = cost
	}
	j.meanRelF += (float64(rel) - j.meanRelF) / n
	j.meanCostF += (float64(cost) - j.meanCostF) / n
	j.sumSqRel += float64(rel) * float64(rel)
	j.MeanRel = time.Duration(j.meanRelF)
	j.MeanCost = time.Duration(j.meanCostF)
	variance := j.sumSqRel/n - j.meanRelF*j.meanRelF
	if variance < 0 {
		variance = 0
	}
	j.StdRel = time.Duration(math.Sqrt(variance))
}
