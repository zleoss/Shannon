// =============================================================================
// 文件: go/orchestrator/tools/replay/main.go
// -----------------------------------------------------------------------------
// 【一句话功能】 Temporal 工作流历史回放工具，验证历史事件与当前代码的非确定性
// 【关键内容】 注册所有已知工作流；从 JSON 文件读取历史；检查非确定性变更
// 【协作关系】 引用 internal/workflows 和 strategies 包，由开发者手动执行
// =============================================================================
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"go.temporal.io/sdk/worker"

	// Register workflows from the orchestrator module
	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/workflows"
	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/workflows/scheduled"
	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/workflows/strategies"
)

func main() {
	historyPath := flag.String("history", "", "Path to Temporal workflow history JSON (from tctl --output json)")
	flag.Parse()

	if *historyPath == "" {
		fmt.Fprintln(os.Stderr, "usage: replay -history /path/to/history.json")
		os.Exit(2)
	}

	// Create a replayer and register all known workflows.
	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflow(workflows.OrchestratorWorkflow)
	replayer.RegisterWorkflow(workflows.SimpleTaskWorkflow)
	replayer.RegisterWorkflow(workflows.SupervisorWorkflow)
	replayer.RegisterWorkflow(workflows.StreamingWorkflow)
	replayer.RegisterWorkflow(workflows.ParallelStreamingWorkflow)
	replayer.RegisterWorkflow(strategies.DAGWorkflow)
	replayer.RegisterWorkflow(strategies.ReactWorkflow)
	replayer.RegisterWorkflow(strategies.ResearchWorkflow)
	replayer.RegisterWorkflow(strategies.ExploratoryWorkflow)
	replayer.RegisterWorkflow(strategies.ScientificWorkflow)
	replayer.RegisterWorkflow(scheduled.ScheduledTaskWorkflow)
	// Approval and budget are now middleware; no separate workflows to register

	// Replay from file; this will error on any non-determinism between history and code.
	if err := replayer.ReplayWorkflowHistoryFromJSONFile(nil, *historyPath); err != nil {
		log.Fatalf("Replay failed (non-deterministic change or invalid history): %v", err)
	}

	log.Printf("Replay succeeded for %s", *historyPath)
}
