package monitor

import "time"

// ===== 监控时间序列（Day 24）=====
//
// 第 1 轮的监控只保留「最近一次采样」与「原始采样切片」，控制台无法回答
// 「过去 15 分钟 CPU 走势如何」这类问题。本轮把它扩展为**可查询的时间序列**：
//
//  1. 每个 Agent 的采样被按固定步长（BucketInterval）归并成桶；
//  2. 每个桶保存 CPU / 内存 / 磁盘的均值与峰值，以及任务成功率；
//  3. 查询按窗口返回连续序列（缺失桶补零值但标记 Empty），
//     保证前端画图时不需要自己对齐时间轴。
//
// 设计约束：不引入任何第三方时序库（依赖面保持可审计），桶聚合在内存中完成。

// BucketInterval 是时间序列的固定桶宽。选择 1 分钟是因为：
// 控制台默认看 15 分钟到 1 小时，1 分钟粒度下最多 60 个点，足够画趋势又不会过密。
const BucketInterval = time.Minute

// SeriesPoint 是时间序列上的一个点（一个聚合桶）。
type SeriesPoint struct {
	// BucketStart 是该桶的起始时刻（UTC，按 BucketInterval 对齐）。
	BucketStart time.Time `json:"bucket_start"`
	// Samples 是该桶内的采样数；为 0 表示该桶没有数据（空桶）。
	Samples int `json:"samples"`
	// Empty 为 true 时该桶无采样，其余数值字段为 0。
	Empty bool `json:"empty"`

	// 资源均值（百分比）。
	CPUAvg    float64 `json:"cpu_avg"`
	MemoryAvg float64 `json:"memory_avg"`
	DiskAvg   float64 `json:"disk_avg"`
	// 资源峰值（百分比）——均值会掩盖短时尖刺，峰值用于告警复盘。
	CPUPeak    float64 `json:"cpu_peak"`
	MemoryPeak float64 `json:"memory_peak"`
	DiskPeak   float64 `json:"disk_peak"`

	// 网络字节数的桶内均值，语义与 GetAggregatedMetrics 保持一致。
	NetworkInAvg  int64 `json:"network_in_avg"`
	NetworkOutAvg int64 `json:"network_out_avg"`

	// TaskSuccessRate 是该桶内的任务成功率（0-1）。
	// 桶内没有任务上报时为 -1，调用方据此区分「成功率 0」与「无数据」。
	TaskSuccessRate float64 `json:"task_success_rate"`
	// Tasks 是该桶内统计的任务总数（成功 + 失败）。
	Tasks int `json:"tasks"`
}

// TimeSeries 是某个 Agent 在查询窗口内的时间序列。
type TimeSeries struct {
	AgentID string `json:"agent_id"`
	// Interval 是桶宽，回显给前端用于对齐时间轴。
	Interval time.Duration `json:"interval"`
	Start    time.Time     `json:"start"`
	End      time.Time     `json:"end"`
	// Points 按 BucketStart 升序，长度 = 窗口内桶数（含空桶）。
	Points []SeriesPoint `json:"points"`

	// 窗口汇总：便于控制台直接展示「均值/峰值/样本量」而无需二次遍历。
	SampleCount int     `json:"sample_count"`
	CPUAvg      float64 `json:"cpu_avg"`
	CPUPeak     float64 `json:"cpu_peak"`
	MemoryAvg   float64 `json:"memory_avg"`
	MemoryPeak  float64 `json:"memory_peak"`
	// TaskSuccessRate 为整窗口的加权成功率；无任务时为 -1。
	TaskSuccessRate float64 `json:"task_success_rate"`
	TaskCount       int     `json:"task_count"`
	// Empty 为 true 表示整个窗口内没有任何采样。
	Empty bool `json:"empty"`
}

// TaskOutcome 是一次任务结果上报，用于计算时间序列里的成功率。
//
// 与 ResourceUsage 分开上报：任务成功率不是资源指标，
// 混进 ResourceUsage 会让原有的聚合语义变得含糊。
type TaskOutcome struct {
	AgentID   string    `json:"agent_id"`
	TaskID    string    `json:"task_id,omitempty"`
	Success   bool      `json:"success"`
	Timestamp time.Time `json:"timestamp"`
}
