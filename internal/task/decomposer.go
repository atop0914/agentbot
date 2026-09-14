package task

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// RuleBasedDecomposer 基于规则的目标拆解器
// 通过关键词分析将高层目标拆解为可执行的子任务序列
type RuleBasedDecomposer struct {
	rules []decomposeRule
}

// decomposeRule 拆解规则
type decomposeRule struct {
	Keywords    []string
	Handler     func(goal string, ctx *decomposeContext) []Subtask
	Description string
}

// decomposeContext 拆解上下文，用于生成唯一 ID
type decomposeContext struct {
	taskID    string
	subtaskNo int
	actionNo  int
}

func (c *decomposeContext) nextSubtaskID() string {
	c.subtaskNo++
	return fmt.Sprintf("%s-st-%03d", c.taskID, c.subtaskNo)
}

func (c *decomposeContext) nextActionID(subtaskID string) string {
	c.actionNo++
	return fmt.Sprintf("%s-act-%03d", subtaskID, c.actionNo)
}

// NewRuleBasedDecomposer 创建基于规则的拆解器
func NewRuleBasedDecomposer() *RuleBasedDecomposer {
	d := &RuleBasedDecomposer{}
	d.initRules()
	return d
}

// initRules 初始化拆解规则
func (d *RuleBasedDecomposer) initRules() {
	d.rules = []decomposeRule{
		{
			Keywords:    []string{"部署", "deploy", "发布", "release"},
			Handler:     d.decomposeDeploy,
			Description: "部署/发布类目标",
		},
		{
			Keywords:    []string{"测试", "test", "验证", "verify"},
			Handler:     d.decomposeTest,
			Description: "测试/验证类目标",
		},
		{
			Keywords:    []string{"文件", "file", "下载", "download", "上传", "upload", "复制", "copy"},
			Handler:     d.decomposeFileOp,
			Description: "文件操作类目标",
		},
		{
			Keywords:    []string{"浏览器", "browser", "网页", "web", "爬取", "crawl", "抓取", "scrape"},
			Handler:     d.decomposeBrowser,
			Description: "浏览器/网页操作类目标",
		},
		{
			Keywords:    []string{"api", "接口", "请求", "request", "调用", "call"},
			Handler:     d.decomposeAPI,
			Description: "API 调用类目标",
		},
		{
			Keywords:    []string{"数据库", "database", "db", "sql", "查询", "query"},
			Handler:     d.decomposeDatabase,
			Description: "数据库操作类目标",
		},
	}
}

// Decompose 将目标拆解为子任务
func (d *RuleBasedDecomposer) Decompose(ctx context.Context, goal string) ([]Subtask, error) {
	if goal == "" {
		return nil, fmt.Errorf("goal is empty")
	}

	dctx := &decomposeContext{
		taskID: uuid.New().String()[:8],
	}

	goalLower := strings.ToLower(goal)

	// 尝试匹配规则
	for _, rule := range d.rules {
		for _, kw := range rule.Keywords {
			if strings.Contains(goalLower, kw) {
				subtasks := rule.Handler(goal, dctx)
				if len(subtasks) > 0 {
					return subtasks, nil
				}
			}
		}
	}

	// 默认拆解策略：分析 → 执行 → 验证
	return d.decomposeDefault(goal, dctx), nil
}

// decomposeDeploy 部署类目标拆解
func (d *RuleBasedDecomposer) decomposeDeploy(goal string, ctx *decomposeContext) []Subtask {
	now := time.Now().UTC()

	st1ID := ctx.nextSubtaskID()
	st2ID := ctx.nextSubtaskID()
	st3ID := ctx.nextSubtaskID()
	st4ID := ctx.nextSubtaskID()

	return []Subtask{
		{
			ID:          st1ID,
			Name:        "环境检查",
			Description: "检查部署环境和依赖是否就绪",
			State:       StatePending,
			Actions: []Action{
				{
					ID:    ctx.nextActionID(st1ID),
					Type:  ActionTerminal,
					Name:  "检查运行环境",
					Params: map[string]string{
						"command": "echo 'Checking environment readiness...'",
					},
					State: StatePending,
				},
			},
			CreatedAt: now,
		},
		{
			ID:          st2ID,
			Name:        "构建项目",
			Description: "编译和构建项目产物",
			State:       StatePending,
			DependsOn:   []string{st1ID},
			Actions: []Action{
				{
					ID:    ctx.nextActionID(st2ID),
					Type:  ActionTerminal,
					Name:  "执行构建",
					Params: map[string]string{
						"command": "echo 'Building project...'",
					},
					State: StatePending,
				},
			},
			CreatedAt: now,
		},
		{
			ID:          st3ID,
			Name:        "运行测试",
			Description: "执行测试套件确保质量",
			State:       StatePending,
			DependsOn:   []string{st2ID},
			Actions: []Action{
				{
					ID:    ctx.nextActionID(st3ID),
					Type:  ActionTerminal,
					Name:  "执行测试",
					Params: map[string]string{
						"command": "echo 'Running tests...'",
					},
					State: StatePending,
				},
			},
			CreatedAt: now,
		},
		{
			ID:          st4ID,
			Name:        "执行部署",
			Description: "将构建产物部署到目标环境",
			State:       StatePending,
			DependsOn:   []string{st3ID},
			Actions: []Action{
				{
					ID:    ctx.nextActionID(st4ID),
					Type:  ActionTerminal,
					Name:  "部署上线",
					Params: map[string]string{
						"command": "echo 'Deploying...'",
					},
					State: StatePending,
				},
			},
			CreatedAt: now,
		},
	}
}

// decomposeTest 测试类目标拆解
func (d *RuleBasedDecomposer) decomposeTest(goal string, ctx *decomposeContext) []Subtask {
	now := time.Now().UTC()

	st1ID := ctx.nextSubtaskID()
	st2ID := ctx.nextSubtaskID()
	st3ID := ctx.nextSubtaskID()

	return []Subtask{
		{
			ID:          st1ID,
			Name:        "准备测试环境",
			Description: "设置测试所需的环境和数据",
			State:       StatePending,
			Actions: []Action{
				{
					ID:    ctx.nextActionID(st1ID),
					Type:  ActionTerminal,
					Name:  "初始化测试环境",
					Params: map[string]string{
						"command": "echo 'Preparing test environment...'",
					},
					State: StatePending,
				},
			},
			CreatedAt: now,
		},
		{
			ID:          st2ID,
			Name:        "执行测试",
			Description: "运行测试用例并收集结果",
			State:       StatePending,
			DependsOn:   []string{st1ID},
			Actions: []Action{
				{
					ID:    ctx.nextActionID(st2ID),
					Type:  ActionTerminal,
					Name:  "运行测试",
					Params: map[string]string{
						"command": "echo 'Running tests...'",
					},
					State: StatePending,
				},
			},
			CreatedAt: now,
		},
		{
			ID:          st3ID,
			Name:        "生成测试报告",
			Description: "汇总测试结果并生成报告",
			State:       StatePending,
			DependsOn:   []string{st2ID},
			Actions: []Action{
				{
					ID:    ctx.nextActionID(st3ID),
					Type:  ActionTerminal,
					Name:  "生成报告",
					Params: map[string]string{
						"command": "echo 'Generating test report...'",
					},
					State: StatePending,
				},
			},
			CreatedAt: now,
		},
	}
}

// decomposeFileOp 文件操作类目标拆解
func (d *RuleBasedDecomposer) decomposeFileOp(goal string, ctx *decomposeContext) []Subtask {
	now := time.Now().UTC()

	st1ID := ctx.nextSubtaskID()
	st2ID := ctx.nextSubtaskID()

	return []Subtask{
		{
			ID:          st1ID,
			Name:        "定位文件",
			Description: "查找和定位目标文件",
			State:       StatePending,
			Actions: []Action{
				{
					ID:    ctx.nextActionID(st1ID),
					Type:  ActionFile,
					Name:  "搜索文件",
					Params: map[string]string{
						"operation": "list",
						"path":      ".",
					},
					State: StatePending,
				},
			},
			CreatedAt: now,
		},
		{
			ID:          st2ID,
			Name:        "执行文件操作",
			Description: "对目标文件执行指定操作",
			State:       StatePending,
			DependsOn:   []string{st1ID},
			Actions: []Action{
				{
					ID:    ctx.nextActionID(st2ID),
					Type:  ActionFile,
					Name:  "处理文件",
					Params: map[string]string{
						"operation": "process",
					},
					State: StatePending,
				},
			},
			CreatedAt: now,
		},
	}
}

// decomposeBrowser 浏览器操作类目标拆解
func (d *RuleBasedDecomposer) decomposeBrowser(goal string, ctx *decomposeContext) []Subtask {
	now := time.Now().UTC()

	st1ID := ctx.nextSubtaskID()
	st2ID := ctx.nextSubtaskID()
	st3ID := ctx.nextSubtaskID()

	return []Subtask{
		{
			ID:          st1ID,
			Name:        "启动浏览器",
			Description: "初始化浏览器实例",
			State:       StatePending,
			Actions: []Action{
				{
					ID:    ctx.nextActionID(st1ID),
					Type:  ActionBrowser,
					Name:  "初始化浏览器",
					Params: map[string]string{
						"action": "launch",
					},
					State: StatePending,
				},
			},
			CreatedAt: now,
		},
		{
			ID:          st2ID,
			Name:        "执行网页操作",
			Description: "在目标页面上执行操作",
			State:       StatePending,
			DependsOn:   []string{st1ID},
			Actions: []Action{
				{
					ID:    ctx.nextActionID(st2ID),
					Type:  ActionBrowser,
					Name:  "页面操作",
					Params: map[string]string{
						"action": "navigate",
					},
					State: StatePending,
				},
			},
			CreatedAt: now,
		},
		{
			ID:          st3ID,
			Name:        "提取数据",
			Description: "从页面提取所需数据",
			State:       StatePending,
			DependsOn:   []string{st2ID},
			Actions: []Action{
				{
					ID:    ctx.nextActionID(st3ID),
					Type:  ActionBrowser,
					Name:  "数据提取",
					Params: map[string]string{
						"action": "extract",
					},
					State: StatePending,
				},
			},
			CreatedAt: now,
		},
	}
}

// decomposeAPI API 调用类目标拆解
func (d *RuleBasedDecomposer) decomposeAPI(goal string, ctx *decomposeContext) []Subtask {
	now := time.Now().UTC()

	st1ID := ctx.nextSubtaskID()
	st2ID := ctx.nextSubtaskID()

	return []Subtask{
		{
			ID:          st1ID,
			Name:        "准备请求",
			Description: "构建 API 请求参数",
			State:       StatePending,
			Actions: []Action{
				{
					ID:    ctx.nextActionID(st1ID),
					Type:  ActionAPI,
					Name:  "构建请求",
					Params: map[string]string{
						"action": "prepare",
					},
					State: StatePending,
				},
			},
			CreatedAt: now,
		},
		{
			ID:          st2ID,
			Name:        "执行请求",
			Description: "发送 API 请求并处理响应",
			State:       StatePending,
			DependsOn:   []string{st1ID},
			Actions: []Action{
				{
					ID:    ctx.nextActionID(st2ID),
					Type:  ActionAPI,
					Name:  "发送请求",
					Params: map[string]string{
						"action": "execute",
					},
					State: StatePending,
				},
			},
			CreatedAt: now,
		},
	}
}

// decomposeDatabase 数据库操作类目标拆解
func (d *RuleBasedDecomposer) decomposeDatabase(goal string, ctx *decomposeContext) []Subtask {
	now := time.Now().UTC()

	st1ID := ctx.nextSubtaskID()
	st2ID := ctx.nextSubtaskID()
	st3ID := ctx.nextSubtaskID()

	return []Subtask{
		{
			ID:          st1ID,
			Name:        "连接数据库",
			Description: "建立数据库连接",
			State:       StatePending,
			Actions: []Action{
				{
					ID:    ctx.nextActionID(st1ID),
					Type:  ActionTerminal,
					Name:  "数据库连接",
					Params: map[string]string{
						"command": "echo 'Connecting to database...'",
					},
					State: StatePending,
				},
			},
			CreatedAt: now,
		},
		{
			ID:          st2ID,
			Name:        "执行查询",
			Description: "执行数据库操作",
			State:       StatePending,
			DependsOn:   []string{st1ID},
			Actions: []Action{
				{
					ID:    ctx.nextActionID(st2ID),
					Type:  ActionTerminal,
					Name:  "执行 SQL",
					Params: map[string]string{
						"command": "echo 'Executing query...'",
					},
					State: StatePending,
				},
			},
			CreatedAt: now,
		},
		{
			ID:          st3ID,
			Name:        "处理结果",
			Description: "处理查询结果",
			State:       StatePending,
			DependsOn:   []string{st2ID},
			Actions: []Action{
				{
					ID:    ctx.nextActionID(st3ID),
					Type:  ActionTerminal,
					Name:  "处理结果",
					Params: map[string]string{
						"command": "echo 'Processing results...'",
					},
					State: StatePending,
				},
			},
			CreatedAt: now,
		},
	}
}

// decomposeDefault 默认拆解策略
func (d *RuleBasedDecomposer) decomposeDefault(goal string, ctx *decomposeContext) []Subtask {
	now := time.Now().UTC()

	st1ID := ctx.nextSubtaskID()
	st2ID := ctx.nextSubtaskID()
	st3ID := ctx.nextSubtaskID()

	return []Subtask{
		{
			ID:          st1ID,
			Name:        "分析目标",
			Description: fmt.Sprintf("分析目标: %s", goal),
			State:       StatePending,
			Actions: []Action{
				{
					ID:    ctx.nextActionID(st1ID),
					Type:  ActionTerminal,
					Name:  "目标分析",
					Params: map[string]string{
						"command": fmt.Sprintf("echo 'Analyzing goal: %s'", goal),
					},
					State: StatePending,
				},
			},
			CreatedAt: now,
		},
		{
			ID:          st2ID,
			Name:        "执行任务",
			Description: "执行核心任务逻辑",
			State:       StatePending,
			DependsOn:   []string{st1ID},
			Actions: []Action{
				{
					ID:    ctx.nextActionID(st2ID),
					Type:  ActionTerminal,
					Name:  "执行核心逻辑",
					Params: map[string]string{
						"command": "echo 'Executing task...'",
					},
					State: StatePending,
				},
			},
			CreatedAt: now,
		},
		{
			ID:          st3ID,
			Name:        "验证结果",
			Description: "验证任务执行结果",
			State:       StatePending,
			DependsOn:   []string{st2ID},
			Actions: []Action{
				{
					ID:    ctx.nextActionID(st3ID),
					Type:  ActionTerminal,
					Name:  "结果验证",
					Params: map[string]string{
						"command": "echo 'Verifying results...'",
					},
					State: StatePending,
				},
			},
			CreatedAt: now,
		},
	}
}
