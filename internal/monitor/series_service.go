package monitor

import (
	"context"
	"fmt"
	"time"
)

// ===== 时间序列服务实现（Day 24）=====

// 时间序列窗口的约束：
//   - 最小 1 个桶，避免空窗口导致除零；
//   - 最多 maxSeriesBuckets 个桶，防止前端传 duration=8760h 把内存打满。
const (
	// MaxSeriesBuckets 是单次查询返回的最大桶数。
	MaxSeriesBuckets = 1440 // 1 分钟桶 × 1440 = 24 小时
	// DefaultSeriesWindow 是未指定窗口时的默认长度。
	DefaultSeriesWindow = 30 * time.Minute
)

// GetTimeSeries 返回 Agent 在 [start, end] 内的时间序列。
//
// 语义约定：
//   - 桶宽固定为 BucketInterval，桶边界按 Unix 时间对齐（不受时区影响）；
//   - 窗口内的空桶也会出现在结果中（Empty=true），前端无需自行补点；
//   - 窗口过大时只保留**最近的** MaxSeriesBuckets 个桶，并让 Start 反映实际起点，
//     避免调用方以为拿到了完整历史。
func (s *service) GetTimeSeries(ctx context.Context, agentID string, start, end time.Time) (*TimeSeries, error) {
	if agentID == "" {
		return nil, fmt.Errorf("monitor: agent id is required")
	}
	if end.IsZero() {
		end = time.Now().UTC()
	}
	if start.IsZero() || !start.Before(end) {
		start = end.Add(-DefaultSeriesWindow)
	}

	// 桶边界对齐到 BucketInterval 的整数倍。
	start = truncateToBucket(start)
	end = truncateToBucket(end)
	totalBuckets := int(end.Sub(start)/BucketInterval) + 1
	if totalBuckets > MaxSeriesBuckets {
		// 只保最近的窗口：把 start 前移到「end 往前 MaxSeriesBuckets-1 个桶」。
		start = end.Add(-time.Duration(MaxSeriesBuckets-1) * BucketInterval)
		totalBuckets = MaxSeriesBuckets
	}

	samples, err := s.repo.QuerySamples(ctx, agentID, start, end)
	if err != nil {
		return nil, err
	}
	outcomes, err := s.repo.QueryTaskOutcomes(ctx, agentID, start, end)
	if err != nil {
		return nil, err
	}

	series := &TimeSeries{
		AgentID:  agentID,
		Interval: BucketInterval,
		Start:    start,
		End:      end,
		Points:   make([]SeriesPoint, totalBuckets),
		// 默认无任务；有任务时会被覆盖。
		TaskSuccessRate: -1,
	}
	for i := range series.Points {
		series.Points[i] = SeriesPoint{
			BucketStart:     start.Add(time.Duration(i) * BucketInterval),
			Empty:           true,
			TaskSuccessRate: -1,
		}
	}

	// 采样按桶归并：均值与峰值。空桶保持 Empty 标记。
	sampleIndex := func(t time.Time) int {
		return int(t.Sub(start) / BucketInterval)
	}
	for _, sample := range samples {
		if sample == nil {
			continue
		}
		idx := sampleIndex(sample.Timestamp)
		if idx < 0 || idx >= len(series.Points) {
			continue
		}
		p := &series.Points[idx]
		p.Empty = false
		p.Samples++
		p.CPUAvg += sample.CPU
		p.MemoryAvg += sample.Memory
		p.DiskAvg += sample.Disk
		p.NetworkInAvg += sample.NetworkIn
		p.NetworkOutAvg += sample.NetworkOut
		if sample.CPU > p.CPUPeak {
			p.CPUPeak = sample.CPU
		}
		if sample.Memory > p.MemoryPeak {
			p.MemoryPeak = sample.Memory
		}
		if sample.Disk > p.DiskPeak {
			p.DiskPeak = sample.Disk
		}
	}

	// 任务结果按桶统计成功率。
	taskTotal := make([]int, len(series.Points))
	taskOK := make([]int, len(series.Points))
	for _, o := range outcomes {
		if o == nil {
			continue
		}
		idx := sampleIndex(o.Timestamp)
		if idx < 0 || idx >= len(series.Points) {
			continue
		}
		taskTotal[idx]++
		if o.Success {
			taskOK[idx]++
		}
	}

	// 收尾：均值除法、成功率、整窗汇总。
	var (
		cpuSum, memSum   float64
		cpuPeak, memPeak float64
		allOK, allTotal  int
	)
	for i := range series.Points {
		p := &series.Points[i]
		if p.Samples > 0 {
			n := float64(p.Samples)
			p.CPUAvg /= n
			p.MemoryAvg /= n
			p.DiskAvg /= n
			p.NetworkInAvg = int64(float64(p.NetworkInAvg) / n)
			p.NetworkOutAvg = int64(float64(p.NetworkOutAvg) / n)
		}
		if taskTotal[i] > 0 {
			p.Tasks = taskTotal[i]
			p.TaskSuccessRate = float64(taskOK[i]) / float64(taskTotal[i])
		}

		series.SampleCount += p.Samples
		cpuSum += p.CPUAvg
		memSum += p.MemoryAvg
		if p.CPUPeak > cpuPeak {
			cpuPeak = p.CPUPeak
		}
		if p.MemoryPeak > memPeak {
			memPeak = p.MemoryPeak
		}
		allOK += taskOK[i]
		allTotal += taskTotal[i]
	}

	series.Empty = series.SampleCount == 0
	series.CPUPeak = cpuPeak
	series.MemoryPeak = memPeak
	// 窗口均值按有数据的桶求平均（分母是「非空桶数」而非「全部桶数」），
	// 否则一台只上报了 1 分钟的 Agent 会因为大量空桶而被平均到接近 0。
	if nonEmpty := countNonEmpty(series.Points); nonEmpty > 0 {
		series.CPUAvg = cpuSum / float64(nonEmpty)
		series.MemoryAvg = memSum / float64(nonEmpty)
	}
	series.TaskCount = allTotal
	if allTotal > 0 {
		series.TaskSuccessRate = float64(allOK) / float64(allTotal)
	}
	return series, nil
}

// GetTimeSeriesByDuration 返回最近 duration 的时间序列。
func (s *service) GetTimeSeriesByDuration(ctx context.Context, agentID string, duration time.Duration) (*TimeSeries, error) {
	if duration <= 0 {
		duration = DefaultSeriesWindow
	}
	end := time.Now().UTC()
	return s.GetTimeSeries(ctx, agentID, end.Add(-duration), end)
}

// ReportTaskOutcome 记录一次任务结果。
//
// 与 ReportStatus 不同，这里**不**触发告警求值：任务成功率告警需要累积足够样本，
// 单次结果不足以判定，避免一次失败就立刻拉响告警。
func (s *service) ReportTaskOutcome(ctx context.Context, outcome TaskOutcome) error {
	if outcome.AgentID == "" {
		return fmt.Errorf("monitor: agent id is required")
	}
	if outcome.Timestamp.IsZero() {
		outcome.Timestamp = time.Now().UTC()
	}
	cp := outcome
	return s.repo.AppendTaskOutcome(ctx, &cp)
}

// truncateToBucket 把时刻向下对齐到桶边界。
func truncateToBucket(t time.Time) time.Time {
	return t.UTC().Truncate(BucketInterval)
}

func countNonEmpty(points []SeriesPoint) int {
	n := 0
	for i := range points {
		if !points[i].Empty {
			n++
		}
	}
	return n
}
