package task

import (
	"context"
	"fmt"
	"sync"
)

// DependencyGraph 表示子任务间的依赖关系图（DAG）
// 用于拓扑排序、环检测、并行调度
type DependencyGraph struct {
	// nodes 存储所有子任务 ID
	nodes []string
	// adj 邻接表：node -> 依赖它的后续节点
	adj map[string][]string
	// reverseAdj 反向邻接表：node -> 它依赖的前置节点
	reverseAdj map[string][]string
	// inDegree 入度表：node -> 剩余未完成的前置依赖数
	inDegree map[string]int
	// subtaskMap ID -> Subtask 指针
	subtaskMap map[string]*Subtask
}

// NewDependencyGraph 从子任务列表构建依赖图
func NewDependencyGraph(subtasks []Subtask) *DependencyGraph {
	g := &DependencyGraph{
		adj:        make(map[string][]string),
		reverseAdj: make(map[string][]string),
		inDegree:   make(map[string]int),
		subtaskMap: make(map[string]*Subtask),
	}

	// 注册所有节点
	for i := range subtasks {
		id := subtasks[i].ID
		g.nodes = append(g.nodes, id)
		g.subtaskMap[id] = &subtasks[i]
		g.inDegree[id] = 0
	}

	// 构建边（依赖关系）
	// DependsOn[A] = [B] 表示 A 依赖 B，即 B -> A
	for i := range subtasks {
		for _, depID := range subtasks[i].DependsOn {
			g.adj[depID] = append(g.adj[depID], subtasks[i].ID)
			g.reverseAdj[subtasks[i].ID] = append(g.reverseAdj[subtasks[i].ID], depID)
			g.inDegree[subtasks[i].ID]++
		}
	}

	return g
}

// DetectCycles 检测依赖图中是否存在环
// 返回 true 表示存在环，同时返回构成环的节点列表
func (g *DependencyGraph) DetectCycles() (bool, []string) {
	// 使用 Kahn 算法：如果拓扑排序不能覆盖所有节点，说明有环
	inDeg := make(map[string]int)
	for k, v := range g.inDegree {
		inDeg[k] = v
	}

	// 收集入度为 0 的节点
	var queue []string
	for _, node := range g.nodes {
		if inDeg[node] == 0 {
			queue = append(queue, node)
		}
	}

	visited := 0
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		visited++

		for _, neighbor := range g.adj[node] {
			inDeg[neighbor]--
			if inDeg[neighbor] == 0 {
				queue = append(queue, neighbor)
			}
		}
	}

	if visited == len(g.nodes) {
		return false, nil
	}

	// 找出环中的节点（入度不为 0 的节点）
	var cycleNodes []string
	for _, node := range g.nodes {
		if inDeg[node] > 0 {
			cycleNodes = append(cycleNodes, node)
		}
	}

	return true, cycleNodes
}

// TopologicalSort 使用 Kahn 算法进行拓扑排序
// 返回按层级分组的子任务 ID（同层内无依赖关系，可并行执行）
// 例如：[[A, B], [C, D], [E]] 表示 A/B 可并行，完成后 C/D 可并行，最后 E
func (g *DependencyGraph) TopologicalSort() ([][]string, error) {
	hasCycle, cycleNodes := g.DetectCycles()
	if hasCycle {
		return nil, fmt.Errorf("circular dependency detected among nodes: %v", cycleNodes)
	}

	inDeg := make(map[string]int)
	for k, v := range g.inDegree {
		inDeg[k] = v
	}

	var levels [][]string
	// 第一层：所有入度为 0 的节点
	var currentLevel []string
	for _, node := range g.nodes {
		if inDeg[node] == 0 {
			currentLevel = append(currentLevel, node)
		}
	}

	for len(currentLevel) > 0 {
		levels = append(levels, currentLevel)
		var nextLevel []string

		for _, node := range currentLevel {
			for _, neighbor := range g.adj[node] {
				inDeg[neighbor]--
				if inDeg[neighbor] == 0 {
					nextLevel = append(nextLevel, neighbor)
				}
			}
		}

		currentLevel = nextLevel
	}

	return levels, nil
}

// GetDependencies 获取指定节点的所有直接前置依赖
func (g *DependencyGraph) GetDependencies(nodeID string) []string {
	return g.reverseAdj[nodeID]
}

// GetDependents 获取依赖指定节点的所有后续节点
func (g *DependencyGraph) GetDependents(nodeID string) []string {
	return g.adj[nodeID]
}

// NodeCount 返回图中节点总数
func (g *DependencyGraph) NodeCount() int {
	return len(g.nodes)
}

// EdgeCount 返回图中边的总数
func (g *DependencyGraph) EdgeCount() int {
	count := 0
	for _, neighbors := range g.adj {
		count += len(neighbors)
	}
	return count
}

// ParallelScheduler 并行任务调度器
// 基于依赖图的层级调度：同层内无依赖的子任务并发执行
type ParallelScheduler struct {
	runner *TaskRunner
	// maxConcurrency 控制每层最大并发数，0 表示无限制
	maxConcurrency int
}

// NewParallelScheduler 创建并行调度器
func NewParallelScheduler(runner *TaskRunner, maxConcurrency int) *ParallelScheduler {
	return &ParallelScheduler{
		runner:         runner,
		maxConcurrency: maxConcurrency,
	}
}

// ScheduleResult 调度执行结果
type ScheduleResult struct {
	// Levels 按执行顺序排列的层级
	Levels [][]string
	// CompletedCount 成功完成的子任务数
	CompletedCount int
	// FailedCount 失败的子任务数
	FailedCount int
	// Errors 各子任务的错误信息
	Errors map[string]error
}

// Schedule 按依赖图的拓扑层级并行执行所有子任务
func (ps *ParallelScheduler) Schedule(t *Task) (*ScheduleResult, error) {
	graph := NewDependencyGraph(t.Subtasks)

	levels, err := graph.TopologicalSort()
	if err != nil {
		return nil, err
	}

	result := &ScheduleResult{
		Levels: levels,
		Errors: make(map[string]error),
	}

	for _, level := range levels {
		if err := ps.executeLevel(t, level, graph, result); err != nil {
			return result, err
		}
	}

	return result, nil
}

// executeLevel 并行执行同一层级的所有子任务
func (ps *ParallelScheduler) executeLevel(t *Task, level []string, graph *DependencyGraph, result *ScheduleResult) error {
	// 确定并发数
	workers := len(level)
	if ps.maxConcurrency > 0 && workers > ps.maxConcurrency {
		workers = ps.maxConcurrency
	}

	// 使用信号量控制并发
	sem := make(chan struct{}, workers)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, nodeID := range level {
		subtask := graph.subtaskMap[nodeID]
		if subtask == nil {
			continue
		}

		// 检查前置依赖是否全部成功
		depsFailed := false
		for _, depID := range graph.GetDependencies(nodeID) {
			if err, ok := result.Errors[depID]; ok && err != nil {
				depsFailed = true
				break
			}
		}

		if depsFailed {
			// 前置依赖失败，跳过此子任务
			subtask.State = StateFailed
			subtask.Error = "dependency failed"
			mu.Lock()
			result.FailedCount++
			result.Errors[nodeID] = fmt.Errorf("skipped: dependency failed")
			mu.Unlock()
			continue
		}

		wg.Add(1)
		go func(st *Subtask) {
			defer wg.Done()

			sem <- struct{}{}        // 获取信号量
			defer func() { <-sem }() // 释放信号量

			if err := ps.runner.RunSubtask(context.Background(), st); err != nil {
				mu.Lock()
				result.FailedCount++
				result.Errors[st.ID] = err
				mu.Unlock()
			} else {
				mu.Lock()
				result.CompletedCount++
				mu.Unlock()
			}
		}(subtask)
	}

	wg.Wait()
	return nil
}
