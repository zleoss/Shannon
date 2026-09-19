// =============================================================================
// 文件: go/orchestrator/internal/constants/activities.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   Activity 名称常量 —— 统一管理所有 Temporal Activity 注册名。
// 【关键内容】
//   AgentExecuteActivity / DecomposeTaskActivity / SynthesizeActivity 等
// 【协作关系】
//   被 workflows 引用通过名称调用 activity，被 worker 注册时使用。
// =============================================================================
package constants

// Activity names used for workflow registration and execution.
// Using constants eliminates magic strings and ensures consistency.
const (
	// Session Management Activities
	UpdateSessionResultActivity = "UpdateSessionResult"

	// Budget Management Activities
	CheckTokenBudgetActivity                   = "CheckTokenBudget"
	CheckTokenBudgetWithBackpressureActivity   = "CheckTokenBudgetWithBackpressure"
	CheckTokenBudgetWithCircuitBreakerActivity = "CheckTokenBudgetWithCircuitBreaker"
	RecordTokenUsageActivity                   = "RecordTokenUsage"
	GenerateUsageReportActivity                = "GenerateUsageReport"
	UpdateBudgetPolicyActivity                 = "UpdateBudgetPolicy"

	// Agent Execution Activities
	ExecuteAgentWithBudgetActivity = "ExecuteAgentWithBudget"

	// Planning/Decomposition Activities
	DecomposeTaskActivity       = "DecomposeTask"
	RefineResearchQueryActivity = "RefineResearchQuery"
	AnalyzeComplexityActivity   = "AnalyzeComplexity" // legacy compatibility for replay

	// Human Intervention Activities
	RequestApprovalActivity         = "RequestApproval"
	ProcessApprovalResponseActivity = "ProcessApprovalResponse"
	GetApprovalStatusActivity       = "GetApprovalStatus"

	// HITL Research Review Activities
	GenerateResearchPlanActivity = "GenerateResearchPlan"

	// Streaming Activities
	StreamExecuteActivity       = "StreamExecute"
	BatchStreamExecuteActivity  = "BatchStreamExecute"
	GetStreamingMetricsActivity = "GetStreamingMetrics"

	// Swarm Agent Activities
	AgentLoopStepActivity = "AgentLoopStep"

	// P2P Activities
	SendAgentMessageActivity   = "SendAgentMessage"
	FetchAgentMessagesActivity = "FetchAgentMessages"
	WorkspaceAppendActivity    = "WorkspaceAppend"
	WorkspaceListActivity      = "WorkspaceList"
	WorkspaceListAllActivity   = "WorkspaceListAll"
	SendTaskRequestActivity    = "SendTaskRequest"
	SendTaskOfferActivity      = "SendTaskOffer"
	SendTaskAcceptActivity     = "SendTaskAccept"

	// File Registry Activities
	RegisterFileActivity    = "RegisterFile"
	GetFileRegistryActivity = "GetFileRegistry"

	// Workspace setup
	SetupWorkspaceDirsActivity = "SetupWorkspaceDirs"

	// TaskList activities
	InitTaskListActivity     = "InitTaskList"
	GetTaskListActivity      = "GetTaskList"
	UpdateTaskStatusActivity = "UpdateTaskStatus"
	CreateTaskActivity            = "CreateTask"
	UpdateTaskDescriptionActivity = "UpdateTaskDescription"
	ClaimTaskActivity             = "ClaimTask"

	// Lead Agent Activities
	LeadDecisionActivity       = "LeadDecision"
	ListWorkspaceFilesActivity = "ListWorkspaceFiles"
	ReadWorkspaceFileActivity  = "ReadWorkspaceFile"
	LeadExecuteToolActivity    = "LeadExecuteTool" // direct tool execution for Lead

	// Schedule Quota Activity
	PauseScheduleForQuotaActivity = "PauseScheduleForQuota"

	// Daemon Dispatch Activity (scheduled task → daemon execution)
	DaemonDispatchActivity = "DaemonDispatch"
)
