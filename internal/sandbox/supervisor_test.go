package sandbox

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestSupervisor_StartStopReapsTree 断言长驻进程被回收的**副作用**。
//
// 为什么不用「Applied 里的字段」来断言：读回来的正是写进去的值，
// 一个完全没生效的实现同样能让它通过。这里看的是进程组里**还有没有活口**。
//
// 被测形态刻意选成「sh -c '(后台循环) & exec sleep 600'」：孙进程继承
// stdout 写端，正是 sandbox.Run 里记过的那个「Wait 被管道永久挂住」的形态。
// 只杀主进程的实现会在这里留下一个还活着的 sleep。
func TestSupervisor_StartStopReapsTree(t *testing.T) {
	m := New(Config{CgroupRoot: "/sys/fs/cgroup", CgroupBase: "agentbot-mcptest", Limits: DefaultLimits()})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sup, err := m.Start(ctx, "mcp-supervisor-test", "", "sh", []string{"-c", "sleep 600"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid := sup.PID()
	if pid <= 0 {
		t.Fatalf("expected a positive pid, got %d", pid)
	}

	cgPath := sup.lim.CgroupPath()

	// 前提断言：进程此刻确实活着。缺了这条，「杀成功」可能只是
	// 因为进程根本没起来过。
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("process %d should be alive right after Start: %v", pid, err)
	}

	pgid := sup.lim.Pgid()

	if err := sup.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// 副作用一：主进程不再存在
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("process %d is still alive after Stop", pid)
	}

	// 副作用二：整个进程组被清空（linux 才有进程组）
	//
	// ⚠️ 必须轮询，不能在 Stop 返回的瞬间断言：SIGKILL 只是「投递了信号」，
	// 内核回收进程与清空进程组是异步的，Stop 返回时组里可能还有一个
	// 正在退出的成员。断言的是「最终没有活口」，不是「立即没有活口」。
	if pgid > 0 {
		groupGone := false
		for time.Now().Before(deadline) {
			if err := syscall.Kill(-pgid, 0); err != nil {
				groupGone = true
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !groupGone {
			out, _ := exec.Command("ps", "-o", "pid,ppid,pgid,stat,cmd", "-g", strconv.Itoa(pgid)).CombinedOutput()
			t.Fatalf("process group %d still has live members after Stop:\n%s", pgid, out)
		}
	}

	// 副作用三：cgroup 目录真的被删掉了（不是「应该被删」）
	if cgPath != "" {
		if _, err := os.Stat(cgPath); !os.IsNotExist(err) {
			t.Errorf("cgroup dir %s should be gone after Stop (stat err=%v)", cgPath, err)
		}
	}

	// 幂等：第二次 Stop 不报错
	if err := sup.Stop(); err != nil {
		t.Errorf("second Stop should be a no-op, got %v", err)
	}
}

// TestSupervisor_WaitReturnsOnSelfExit 覆盖「进程自己退出」这条路径 ——
// 崩溃的 MCP server 走的就是它，客户端必须能立刻知道，而不是等到超时。
func TestSupervisor_WaitReturnsOnSelfExit(t *testing.T) {
	m := New(Config{CgroupRoot: "/sys/fs/cgroup", CgroupBase: "agentbot-mcptest", Limits: DefaultLimits()})

	sup, err := m.Start(context.Background(), "mcp-exit-test", "", "sh", []string{"-c", "exit 7"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Stop 必须在「进程已自行退出」时不阻塞。
	// 回归的是实测到的缺陷：无条件 drainWait 会让每次崩溃多等满 3 秒，
	// 而崩溃是长驻 server 最常见的收场。阈值取 1s（远大于正常路径的 µs 级）。
	defer func() {
		start := time.Now()
		_ = sup.Stop()
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("Stop on an already-exited process took %v; it must not block", elapsed)
		}
	}()

	done := make(chan error, 1)
	go func() { done <- sup.Wait() }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a non-nil error for exit status 7")
		}
		if !strings.Contains(err.Error(), "exit status 7") {
			t.Errorf("expected exit status 7 in error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after the process exited")
	}
}

// TestSupervisor_ContextCancelStopsProcess 断言取消 ctx 会真的杀掉进程 ——
// 这条是「客户端取消一次长跑工具调用」时唯一的回收路径。
func TestSupervisor_ContextCancelStopsProcess(t *testing.T) {
	m := New(Config{CgroupRoot: "/sys/fs/cgroup", CgroupBase: "agentbot-mcptest", Limits: DefaultLimits()})

	ctx, cancel := context.WithCancel(context.Background())
	sup, err := m.Start(ctx, "mcp-cancel-test", "", "sh", []string{"-c", "sleep 600"})
	if err != nil {
		cancel()
		t.Fatalf("Start: %v", err)
	}
	pid := sup.PID()

	cancel()

	waitDone := make(chan struct{})
	go func() {
		_ = sup.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatalf("process %d did not exit after ctx cancel", pid)
	}
	_ = sup.Stop()

	pidFile := strconv.Itoa(pid)
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("process %s still alive after ctx cancel", pidFile)
	}
}

// TestSupervisor_RequiresProgramName 覆盖调用方错误：
// 空程序名必须立刻失败，而不是启动一个空进程再超时。
func TestSupervisor_RequiresProgramName(t *testing.T) {
	m := New(DefaultConfig())
	if _, err := m.Start(context.Background(), "agent", "", "", nil); err == nil {
		t.Fatal("expected an error for an empty program name")
	}
}
