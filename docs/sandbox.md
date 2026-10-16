# 进程级沙箱（internal/sandbox）

## 为什么是进程级，不是容器

原计划让每个 Agent 跑在独立容器里。本机**没有 Docker**
（`docker: command not found`，无 `/var/run/docker.sock`），
容器方案无法落地（也没有 rootless Docker）。

`internal/sandbox` 是它的**等价替代**：用进程级手段达成同样的工程目标 ——
「一个 Agent 不能耗光宿主机资源、不能越界读写别的 Agent 的文件」。

---

## ⚠️ 先读这一节：这不是安全边界

**逐条写明拦不住什么**。高估隔离强度比没有隔离更危险 ——
前者会让人放心地把不信任的代码放进来跑。

| 场景 | 能否拦住 | 说明 |
|------|---------|------|
| 意外死循环 `while :; do :; done` 吃满 CPU | ✅ | cgroup cpu.max / RLIMIT_CPU 打断，实测 1.0s |
| 意外把内存吃到爆 | ✅ | RLIMIT_AS 先触发（返回 ENOMEM），cgroup memory.max 作后备 |
| 意外 fork 炸弹 | ✅ | cgroup pids.max，实测有 fork 被拒 |
| 意外写满磁盘（单文件） | ✅ | RLIMIT_FSIZE，实测写入被截断 |
| 意外写到别的 Agent 目录 | ✅ | 每 Agent 独立工作目录 + filepath.Rel 判定 |
| 意外留下失控子进程 | ✅ | Setpgid + 超时按进程组回收，实测孤儿已被清理 |
| **恶意进程主动逃逸** | ❌ | 同 uid，可 `kill` 别的进程、读 `/proc/<pid>/environ` |
| **读宿主机文件（同权限）** | ❌ | 进程以 root 跑，能读 `/etc/shadow` 以外的一切 |
| **绕过 cgroup 限制** | ❌ | 同 uid 可写 `cgroup.procs` 把自己移出去 |
| **软链接逃逸工作目录** | ❌ | 路径判定只做词法层，未解析 symlink |
| **网络访问** | ❌ | 本包**完全不管**网络（见 `internal/network`） |
| **横向访问同 uid 的其他服务** | ❌ | 无 user namespace、无 net namespace |

**一句话**：它防「意外」，不防「恶意」。
要真正跑不可信代码，需要容器或 VM；本包只适用于**受信 Agent + 资源保护**场景。

---

## 两层资源限制

同时施加两层，因为它们约束的**不是同一件事**：

### 第一层：cgroup v2

| 文件 | 值 | 作用 |
|------|-----|------|
| `cpu.max` | `<quota×1e6> 1000000` | 每秒可消耗的 CPU 时间 |
| `memory.max` | MemoryBytes | 驻留物理内存上限，超过触发 OOM kill |
| `memory.swap.max` | `0` | **防止限额被 swap 架空** |
| `pids.max` | MaxProcesses | 进程/线程总数 |

关键实现细节：

1. **`SysProcAttr.CgroupFD` 消除 `fork → attach` 时间窗**。
   常见写法是 `Start()` 之后再往 `cgroup.procs` 写 pid，中间那段进程
   不受约束。CgroupFD 让内核在 clone 时就放进目标 cgroup ——
   测试里进程读自己的 `/proc/self/cgroup` 直接得到 `/agentbot-sandbox/xxx`。

2. **控制器下放**：必须往父目录的 `cgroup.subtree_control` 写 `+cpu +memory +pids`，
   否则子目录里这些文件不存在，写入报 "no such file"。

3. **逐个控制器写、逐个判定**：一次性写多个时只要一个不可用就全失败，
   会丢掉其余可用的控制器。部分成功要如实上报（`Applied.partial`）。

### 第二层：rlimit

| 资源 | 对应 | 作用 |
|------|------|------|
| `RLIMIT_CPU` | `ulimit -t` | 累计 CPU 秒数 |
| `RLIMIT_AS` | `ulimit -v` | **虚拟地址空间**上限 |
| `RLIMIT_FSIZE` | `ulimit -f` | 单文件字节数 |
| `RLIMIT_NOFILE` | `ulimit -n` | 文件描述符数 |

关键实现细节：

1. **必须在子进程内设置**。本 Go 版本的 `syscall.SysProcAttr` **没有** Rlimits 字段，
   所以改用「`ulimit` 引导脚本 + `exec` 原地替换」：
   ```sh
   ulimit -c 0; ulimit -t 60; ulimit -v 65536; ...; exec sh -c '<真任务>'
   ```
   `exec` 让真任务继承限制并复用同一 pid。
   **在父进程里调 `Setrlimit` 改的是父进程自己，对子进程毫无作用** ——
   一个代码看起来对了、实际毫无边界的经典写法。

2. **`ulimit -c 0` 关 core dump**：被监管进程崩溃时不该把宿主磁盘写满。

### ⚠️ 实测事实：RLIMIT_AS 先于 cgroup OOM 触发

这条是**实测得出**的，不是推断，而且它决定了两层限制的实际分工：

```
RLIMIT_AS  → 约束**虚拟地址空间**  → mmap 返回 ENOMEM
cgroup     → 约束**驻留物理内存**  → OOM killer 介入
```

运行时的 VA 开销远大于 RSS，所以 VA 上限总会先被撞到。实测一个按页
分配 4 MiB 块的 python3 进程在 **32 MiB 处**就收到 `MemoryError`，
而 cgroup 的 `memory.peak` 从未接近 64 MiB 限额、`oom_kill` 保持 0。

**所以：内存的实际约束者是 RLIMIT_AS，cgroup memory.max 是后备闸门。**
测试因此断言「分配被拒绝」+「第二道闸门确实挂上了」，
而不是断言一个通常不会发生的 OOM kill。

---

## ⚠️ 实测暴露的三个真 bug（都已修，都不是新写的代码）

### 1. `cmd.Wait()` 会被孙进程持有的管道写端永久挂住

脚本形如 `(后台循环) & sleep 30` 时 Wait 不返回。
**实测：把 sh 单独 SIGKILL 之后，Wait 仍然阻塞** ——
因为 Wait 等的不只是进程退出，还包括**输出管道被关闭**，
而后台孙进程继承了 stdout 的写端。

超时路径也救不了：`exec.CommandContext` 的默认 cancel 只杀主进程，
孙进程照样活着持有写端。结果是一个吃满 CPU 的孤儿 + 一个永不返回的调用。

本仓库 executor 会在 `sh -c` 下跑 go build / npm install，
这类工具链大量使用后台任务，撞上的概率不低。

**修法**：给 Wait 加 wall-clock 上限，超时后按进程组杀整棵树，
再等管道自然关闭。

### 2. `KillGroup` 是个静默 no-op

原实现每次现问 `syscall.Getpgid(l.pid)`。主进程被回收后
`Getpgid` 返回 `ESRCH`，函数走进「进程可能已退出」分支**直接 `return nil`**。
调用方看到成功，进程组里却一个都没死。

实测现场：主进程 pid 已被回收，`ps -g <pgid>` 里还躺着一个 `sleep 0.2` 循环。

**修法**：pgid 在进程启动那一刻就缓存（`Track`）；
清理失败记进 `Warnings` 而不是报平安。

### 3. 被超时中断的命令返回 `exit=0`

`sh -c "cmd & sleep 300"` 的 sh 在被打断时 `waitErr` 可能为 nil，
于是 `exitCode` 是 0 —— **调用方把「被中断」读成「成功完成」**，
进而把只做了一半的任务标记为已完成。比报错更糟。

**修法**：超时中断统一返回 `124`（coreutils `timeout(1)` 的约定，运维一眼能认出）。

---

## 文件系统边界

`Workspace` 给每个 Agent 一个**独立工作目录**（`<root>/<agent-id>/`）。

**与「共享根目录 + 路径前缀校验」的区别**（设计上的关键选择）：
前缀校验方案里隔离强度完全依赖那段校验代码的正确性 ——
本仓库 `internal/filesystem` 就曾因 `strings.HasPrefix` 判定被**兄弟目录绕过**
（`/root/fs-evil` 对 base `/root/fs` 的 `HasPrefix` 为 true）。
独立目录让「隔离」变成文件系统本身的性质：越界不是被"拦住"的，
而是**根本不共享前缀**。

这不能替代路径校验（同一 Workspace 内的 `../` 仍需拦），
但它让单点校验的失败不再等于跨 Agent 泄漏。

判定统一用 `filepath.Rel`（`withinBase` / `Resolve`），不用字符串前缀。

---

## 用法

```go
mgr := sandbox.New(sandbox.DefaultConfig())   // 初始化不探测 cgroup，见下

res, err := mgr.Run(ctx, agentID, workDir, `go build ./...`, 5*time.Minute)
if err != nil { ... }

// res.Applied 描述**实际生效**的限制（事实，不是请求）
if !res.Applied.AnyEnforced() {
    // 毫无边界的执行 —— 应当拒绝
}
log.Printf("sandbox: %s", res.Applied)
// → backend=cgroup-v2 cpu=on mem=on pids=on rlimit=on fsize=on nofile=on
```

**为什么 `New` 不探测 cgroup**：探测必须发生在真正的 Apply 时刻。
「此刻可写」不代表「整个进程生命周期内可写」（cgroup 可能被宿主上
其他程序重新挂载）。把能力探测与使用分开，会产出
「启动时说支持、运行时报错」这种最难查的一类问题。

### 降级路径

cgroup 不可用时降级到 rlimit-only，并**必须**留下 `Warnings`：

```
backend=rlimit cpu=off mem=off pids=off rlimit=on fsize=on nofile=on warnings=1
```

调用方据此能区分「真被限制了」与「降级了」。
两项都没用上时 `Prepare` 直接报错，而不是返回一个空壳。

---

## 测试纪律

全部断言**副作用**，不是配置值：

| 限制 | 副作用证据 |
|------|-----------|
| CPU | 死循环在 1.0s 被打断（exit=137），而非跑满 20s wall-clock |
| 内存 | 进程自报 `DENIED at 32 MiB`；cgroup peak 远低于请求量 |
| 文件大小 | 磁盘上文件的**真实大小**被截断（不是看退出码） |
| 进程数 | 内核 `pids.events` 里 max 计数 > 0 |
| cgroup 归属 | 进程读自己的 `/proc/self/cgroup` 含 `agentbot-sandbox` |
| 进程组回收 | 调用返回后按唯一标记 `ps` 搜索，**无残留** |
| 方向性 | 反向断言：逃逸目标文件不存在；降级时无 cgroup 路径 |

「`Applied.CPUEnforced() == true`」这类断言毫无价值 ——
它读回来的正是写进去的值，一个完全没生效的实现同样能让它通过。

`-race` 全绿，0 SKIP。cgroup 不可用的环境用 `t.Skipf` 显式说明，
不静默跳过。

---

## 与容器方案的差距（迁移时的注意事项）

| 维度 | 本包 | 容器 |
|------|------|------|
| 资源限制 | cgroup v2 + rlimit | 同左（+ 内核 namespace 加持） |
| 文件系统隔离 | 工作目录 + 词法路径校验 | 挂载 namespace，看不到宿主 |
| 进程隔离 | 进程组（可跨组发信号） | pid namespace |
| 网络隔离 | **无** | net namespace |
| 用户隔离 | **无**（同 uid=root） | user namespace |
| 清理 | 尽力而为（进程组 SIGKILL） | 销毁容器即全部回收 |
| 恶意代码 | **不防** | 基本可防 |

迁移到容器时，`Applied` 的两个断言（「限制真的生效」+
「进程真的在里面」）应当保留 —— 它们是跨实现都成立的契约。
