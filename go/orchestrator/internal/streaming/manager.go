// =============================================================================
// 文件: go/orchestrator/internal/streaming/manager.go
// =============================================================================
//  Stream 事件管理器 —— 核心职责：
//   1. Redis Streams 发布/订阅 —— 跨进程事件分发（go -> python, go->go）
//   2. Ring Buffer（内存通道）—— 每个订阅者一个 goroutine + channel
//   3. Event Store（PostgreSQL 异步批量持久化）—— 回放 / 审计用
//
// 设计模式：
//   - 全局单例（sync.Once），所有组件共享一个 Manager 实例
//   - 订阅者模式：Subscribe() 返回 channel，Unsubscribe() 清理资源
//   - 每个订阅启动一个独立 goroutine（streamReaderFrom）消费 Redis Stream
//   - 持久化走异步批处理（persistWorker），避免阻塞发布链路
//
// 协作关系：
//   httpapi（SSE/WebSocket） -> Subscribe() 获取事件流推送给前端
//   server / orchestrator    -> Publish() 发布工作流事件
//   gateway                  -> 通过 Redis Stream 直接写入事件
//   ReplaySince/ReplayFromStreamID -> 历史回放（断线重连 / 页面刷新）
//
// 关键约束：
//   - 调用者不得关闭 channel，channel 生命周期归 reader goroutine 所有
//   - 所有公开方法都是 goroutine-safe
//   - DB 去重依赖 (workflow_id, seq, type) 唯一索引
// =============================================================================

package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"time"
	"unicode/utf8"

	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/db"
	"github.com/go-redis/redis/v8"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
//  常量
// ---------------------------------------------------------------------------

// globalNotificationMaxLen —— 全局通知 Redis Stream 的最大长度（近似裁剪）
const globalNotificationMaxLen = 10000

// ---------------------------------------------------------------------------
//  核心类型定义
// ---------------------------------------------------------------------------

// Event 是系统中最小的流式事件单元。
// 它既是 SSE 发送给前端的载荷，也是 Redis Stream 中的一条消息，
// 同时也是持久化到 PostgreSQL 的一条记录。
//
// 字段说明：
//   WorkflowID —— 所属工作流 ID，所有事件的关联键
//   Type       —— 事件类型（如 "AGENT_THINKING"、"TOOL_INVOKED"、"WORKFLOW_COMPLETED"）
//   AgentID    —— 产生事件的 Agent（可选，为空时表示系统级事件）
//   Message    —— 人类可读的消息文本（可能包含截断的 base64 图片）
//   Payload    —— 结构化载荷（如工具调用的输入/输出、Agent 的思考内容等）
//   Timestamp  —— 事件发生时间戳（纳秒精度）
//   Seq        —— 工作流内的单调递增序列号，用于去重和顺序判断
//   StreamID   —— Redis Stream 消息 ID（"<timestamp>-<seq>" 格式），用于精确回放
type Event struct {
	WorkflowID string                 `json:"workflow_id"`
	Type       string                 `json:"type"`
	AgentID    string                 `json:"agent_id,omitempty"`
	Message    string                 `json:"message,omitempty"`
	Payload    map[string]interface{} `json:"payload,omitempty"`
	Timestamp  time.Time              `json:"timestamp"`
	Seq        uint64                 `json:"seq"`
	StreamID   string                 `json:"stream_id,omitempty"`
}

// subscription 记录一个订阅者及其取消函数。
// 每个订阅者有一个独立的 context，通过 cancel() 可以精准停止其 reader goroutine。
type subscription struct {
	cancel context.CancelFunc
}

// Manager 是流式事件的核心管理器。采用 Redis Streams 作为跨进程事件总线。
//
// 内部架构：
//                ┌──────────────────┐
//                │   Publish()      │  ← workflow、server、gateway 调用
//                └────────┬─────────┘
//                         │
//          ┌──────────────┼──────────────┐
//          ▼              ▼              ▼
//   ┌────────────┐ ┌────────────┐ ┌──────────────┐
//   │ Redis      │ │ 全局通知   │ │ 本地 Subscriber │
//   │ Stream     │ │ Stream     │ │ (channel)    │
//   └────────────┘ └────────────┘ └──────────────┘
//          │                          │
//          ▼                          ▼
//   ┌──────────────────┐    ┌──────────────────┐
//   │ streamReaderFrom │    │ 与其他进程共享     │
//   │ (goroutine)      │    │                  │
//   └──────────────────┘    └──────────────────┘
//          │
//          ├──▶ subscriber channel → SSE / WebSocket
//          └──▶ enqueuePersistEvent → persistWorker → PostgreSQL
//
// 工作流：
//   1. 发布者通过 Publish() 发布事件
//   2. Publish() 将事件写入 Redis Stream（同时设置 24h TTL）
//   3. 对于终端事件（COMPLETED/FAILED），额外写入全局通知 Stream
//   4. Publish() 将事件加入持久化队列（异步批量写入 PostgreSQL）
//   5. 每个订阅者的 streamReaderFrom goroutine 从 Redis Stream XRead 阻塞读取
//   6. 读取到的事件推送到 subscriber channel，同时也入持久化队列
//   7. 前端通过 SSE 从 subscriber channel 消费事件
//
// 线程安全：所有方法通过 sync.RWMutex 保护，goroutine-safe。
//
// 生命周期管理：
//   初始化：Get() → InitializeRedis() → InitializeEventStore()
//   运行中：Subscribe / Publish / Unsubscribe
//   关闭：  Shutdown() —— 先关 shutdownCh 信号，再等所有 goroutine 退出
type Manager struct {
	// mu 保护 subscribers 和 redis 等字段的并发访问
	mu sync.RWMutex

	// redis —— Redis 客户端。为 nil 时降级为纯内存模式（仅用于开发和测试）
	redis *redis.Client

	// dbClient —— PostgreSQL 客户端。为 nil 时不启用持久化
	dbClient *db.Client

	// persistCh —— 事件持久化通道，容量 = batchSize * 4
	//   Publish() 和 streamReaderFrom 向此通道发送事件
	//   persistWorker 从此通道批量接收并写入 PostgreSQL
	persistCh chan db.EventLog

	// persistClosed —— 标记持久化通道是否已关闭，防止关闭后继续写入
	persistClosed bool

	// persistMu —— 保护 persistCh 的关闭操作，确保与写入操作互斥
	persistMu sync.Mutex

	// batchSize —— 事件持久化的批量大小，达到此数量时立即刷入 PostgreSQL
	batchSize int

	// flushEvery —— 事件持久化的最长时间间隔，即使未达到 batchSize 也会刷入
	flushEvery time.Duration

	// subscribers —— 订阅者映射表
	//   外层 key: workflowID
	//   内层 key:  subscriber channel（用于查找和取消）
	//   内层 value: subscription（含 cancel 函数）
	//
	// 注意：同一个 workflowID 可以有多个订阅者（如多个前端页面同时查看）
	subscribers map[string]map[chan Event]*subscription

	// capacity —— Redis Stream 的近似最大长度（MaxLen ≈）
	capacity int

	// logger —— 日志记录器
	logger *zap.Logger

	// shutdownCh —— 关闭信号通道。close 此通道会通知所有 reader goroutine 退出
	shutdownCh chan struct{}

	// wg —— 等待所有 streamReaderFrom goroutine 退出
	wg sync.WaitGroup

	// persistWg —— 等待 persistWorker goroutine 退出
	persistWg sync.WaitGroup
}

// ---------------------------------------------------------------------------
//  全局单例
// ---------------------------------------------------------------------------

var (
	defaultMgr      *Manager    // 全局唯一的 Manager 实例
	once            sync.Once   // 确保 Manager 只初始化一次
	defaultCapacity = 256       // 默认的 Redis Stream 容量
)

// Get 返回全局唯一的 Manager 实例（惰性初始化模式）。
// 
// 实现细节：
//   使用 sync.Once 确保线程安全的单例创建。
//   首次调用时创建空壳 Manager（仅有 subscribers map 和 shutdownCh），
//   后续通过 InitializeRedis / InitializeEventStore 注入依赖。
//
// 为什么不用 init() 函数？
//   因为 Manager 依赖外部注入（Redis 客户端、DB 客户端），
//   这些依赖在程序启动流程中逐步就绪，不在包初始化阶段就绪。
func Get() *Manager {
	once.Do(func() {
		defaultMgr = &Manager{
			subscribers: make(map[string]map[chan Event]*subscription),
			capacity:    defaultCapacity,
			logger:      zap.L(),
			shutdownCh:  make(chan struct{}),
		}
	})
	return defaultMgr
}

// ---------------------------------------------------------------------------
//  初始化方法
// ---------------------------------------------------------------------------

// InitializeRedis 向全局 Manager 注入 Redis 客户端。
//
// 调用时机：程序启动时，Redis 连接就绪后立即调用。
// 幂等性：可以多次调用，但只有第一次有效（后续调用仅更新 logger）。
func InitializeRedis(redisClient *redis.Client, logger *zap.Logger) {
	if defaultMgr == nil {
		Get()
	}
	defaultMgr.mu.Lock()
	defer defaultMgr.mu.Unlock()
	defaultMgr.redis = redisClient
	if logger != nil {
		defaultMgr.logger = logger
	}
}

// InitializeEventStore 初始化事件持久化存储（PostgreSQL）。
//
// 调用时机：数据库连接就绪后调用。
// 副作用：
//   1. 设置 dbClient
//   2. 创建持久化通道 persistCh
//   3. 启动 persistWorker goroutine（异步批量写入）
//
// 环境变量配置：
//   EVENTLOG_BATCH_SIZE      —— 批量大小（默认 100）
//   EVENTLOG_BATCH_INTERVAL_MS —— 刷新间隔毫秒（默认 100ms）
//
// 幂等性：可以多次调用，但持久化通道和 worker 只启动一次。
func InitializeEventStore(store *db.Client, logger *zap.Logger) {
	if defaultMgr == nil {
		Get()
	}
	defaultMgr.mu.Lock()
	defer defaultMgr.mu.Unlock()
	defaultMgr.dbClient = store
	if logger != nil {
		defaultMgr.logger = logger
	}
	if defaultMgr.persistCh == nil {
		bs := 100
		if v := os.Getenv("EVENTLOG_BATCH_SIZE"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				bs = n
			}
		}
		iv := 100 * time.Millisecond
		if v := os.Getenv("EVENTLOG_BATCH_INTERVAL_MS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				iv = time.Duration(n) * time.Millisecond
			}
		}
		defaultMgr.persistCh = make(chan db.EventLog, bs*4)
		defaultMgr.batchSize = bs
		defaultMgr.flushEvery = iv
		defaultMgr.persistWg.Add(1)
		go defaultMgr.persistWorker()
		defaultMgr.logger.Info("Initialized event log batcher", zap.Int("batch_size", bs), zap.Duration("interval", iv))
	}
}

// Configure 配置默认容量，可在初始化之前或之后调用。
//  capacity —— Redis Stream 的近似最大长度和 subscriber channel 缓冲区大小。
func Configure(capacity int) {
	if capacity <= 0 {
		return
	}
	defaultCapacity = capacity
	if defaultMgr != nil {
		defaultMgr.mu.Lock()
		defaultMgr.capacity = capacity
		defaultMgr.mu.Unlock()
	}
}

// ---------------------------------------------------------------------------
//  Redis Key 辅助函数
// ---------------------------------------------------------------------------

// streamKey 返回某个工作流的事件流在 Redis 中的 key。
// 格式：shannon:workflow:events:<workflowID>
// 命名空间 'shannon:workflow:events:' 用于与系统中其他 Redis key 隔离。
func (m *Manager) streamKey(workflowID string) string {
	return fmt.Sprintf("shannon:workflow:events:%s", workflowID)
}

// seqKey 返回某个工作流的序列号计数器在 Redis 中的 key。
// 格式：shannon:workflow:events:<workflowID>:seq
// 使用 Redis INCR 命令实现单调递增序列号，保证全局有序。
func (m *Manager) seqKey(workflowID string) string {
	return fmt.Sprintf("shannon:workflow:events:%s:seq", workflowID)
}

// ---------------------------------------------------------------------------
//  订阅管理
// ---------------------------------------------------------------------------

// Subscribe 为指定工作流创建一个订阅通道，从 Redis Stream 头部开始消费。
//
// 参数：
//   workflowID —— 要订阅的工作流 ID
//   buffer    —— 通道缓冲区大小
//
// 返回值：chan Event —— 调用者从此通道接收事件。调用者必须 drain（消费）此通道，
//          否则 reader goroutine 会被阻塞。不再需要时调用 Unsubscribe()。
//
// 使用规范：
//   1. 不要关闭返回的 channel —— reader goroutine 拥有它
//   2. 用 for range 或 for select 循环消费
//   3. 结束后务必 Unsubscribe()，否则 goroutine 泄漏
//
// 示例：
//   ch := mgr.Subscribe("wf-123", 100)
//   go func() {
//       for evt := range ch {
//           // 处理事件
//       }
//   }()
//   // ... 一段时间后 ...
//   mgr.Unsubscribe("wf-123", ch)
func (m *Manager) Subscribe(workflowID string, buffer int) chan Event {
	return m.SubscribeFrom(workflowID, buffer, "0-0")
}

// SubscribeFrom 创建订阅并从指定的 Redis Stream ID 位置开始消费。
//
// 用途：
//   - 断线重连：从上次断开的位置继续消费
//   - 页面刷新：从已知的最后一条消息之后开始
//   - 历史回放：指定特定的起始位置
//
// 参数：
//   startID —— Redis Stream 消息 ID（"0-0" 表示从头开始）
//              格式为 "<millisecondsTime>-<sequenceNumber>"
//              也可以使用 "(" 前缀表示排除此 ID
func (m *Manager) SubscribeFrom(workflowID string, buffer int, startID string) chan Event {
	ch := make(chan Event, buffer)

	// 为每个订阅创建独立的 context，用于精确控制 goroutine 生命周期
	ctx, cancel := context.WithCancel(context.Background())

	m.mu.Lock()
	subs := m.subscribers[workflowID]
	if subs == nil {
		subs = make(map[chan Event]*subscription)
		m.subscribers[workflowID] = subs
	}
	subs[ch] = &subscription{cancel: cancel}
	m.mu.Unlock()

	// 启动 Redis Stream 读取 goroutine
	m.wg.Add(1)
	go m.streamReaderFrom(ctx, workflowID, ch, startID)

	return ch
}

// streamReaderFrom —— 核心的 Redis Stream 消费协程。
//
// 这是系统中最关键的 goroutine 之一，每个订阅者对应一个实例。
//
// 工作流程：
//   1. 使用 XRead 阻塞读取（Block: 5s），支持超时和上下文取消
//   2. 读取到新消息后，解析为 Event 结构体
//   3. 对于需要持久化的事件类型，异步加入持久化队列
//   4. 将事件推送到 subscriber channel（非阻塞发送，防死锁）
//   5. 通道满时根据事件严重级别记录不同级别的日志
//   6. 使用指数退避策略处理 Redis 连接错误
//
// 退出条件（任一满足即退出）：
//   a. context 被取消（Unsubscribe() 调用）
//   b. 全局 shutdownCh 被关闭（Manager.Shutdown() 调用）
//   c. ctx 的 Deadline 已过
//
// 资源释放：
//   退出前通过 defer close(ch) 关闭 channel，
//   消费方会收到 channel 关闭信号，可结束 for range 循环。
func (m *Manager) streamReaderFrom(ctx context.Context, workflowID string, ch chan Event, startID string) {
	defer m.wg.Done()
	defer close(ch)

	if m.redis == nil {
		// 无 Redis 时的降级模式：保持通道存活但永不发送数据，
		// 直到 context 被取消或 shutdown。
		// 这在纯开发环境或测试中有用。
		select {
		case <-ctx.Done():
		case <-m.shutdownCh:
		}
		return
	}

	streamKey := m.streamKey(workflowID)
	lastID := startID
	retryDelay := time.Second        // 初始重试延迟
	maxRetryDelay := 30 * time.Second // 最大重试延迟（指数退避上限）

	m.logger.Debug("Starting stream reader",
		zap.String("workflow_id", workflowID),
		zap.String("stream_key", streamKey),
		zap.String("start_id", lastID))

	for {
		// 检查退出信号（非阻塞，避免在 XRead 阻塞时无法响应取消）
		select {
		case <-ctx.Done():
			m.logger.Debug("Stream reader stopping - context cancelled",
				zap.String("workflow_id", workflowID))
			return
		case <-m.shutdownCh:
			m.logger.Debug("Stream reader stopping - manager shutdown",
				zap.String("workflow_id", workflowID))
			return
		default:
		}

		// 从 Redis Stream 阻塞读取新消息
		// XRead 参数说明：
		//   Streams: [key, id] —— 从哪个 stream 的哪个 ID 之后读取
		//   Count:   10  —— 每次最多返回 10 条消息（批量处理）
		//   Block:   5s  —— 无消息时阻塞等待最多 5 秒
		result, err := m.redis.XRead(ctx, &redis.XReadArgs{
			Streams: []string{streamKey, lastID},
			Count:   10,
			Block:   5 * time.Second,
		}).Result()

		if err == redis.Nil {
			// 阻塞超时，没有新消息 —— 正常情况，继续循环
			retryDelay = time.Second
			continue
		}

		if err != nil {
			// 判断错误是否由 context 取消引起（避免误报）
			if ctx.Err() != nil {
				return
			}

			m.logger.Error("Failed to read from Redis stream",
				zap.String("workflow_id", workflowID),
				zap.String("stream_key", streamKey),
				zap.String("last_id", lastID),
				zap.Duration("retry_in", retryDelay),
				zap.Error(err))

			// 指数退避等待后重试，同时监听取消信号
			select {
			case <-time.After(retryDelay):
				retryDelay = min(retryDelay*2, maxRetryDelay) // 退避：1s → 2s → 4s ... → 30s
			case <-ctx.Done():
				return
			case <-m.shutdownCh:
				return
			}
			continue
		}

		// 读取成功，重置退避
		retryDelay = time.Second

		// 处理返回的所有消息
		for _, stream := range result {
			for _, message := range stream.Messages {
				lastID = message.ID // 更新 lastID 实现"至少一次"语义

				// 从 Redis Stream 的 key-value pairs 解析出 Event 结构体
				event := Event{
					WorkflowID: workflowID,
					StreamID:   message.ID,
				}

				// Redis Stream 中存储的是扁平化的字符串键值对，
				// 需要手动解析各字段
				if v, ok := message.Values["type"].(string); ok {
					event.Type = v
				}
				if v, ok := message.Values["agent_id"].(string); ok {
					event.AgentID = v
				}
				if v, ok := message.Values["message"].(string); ok {
					event.Message = v
				}
				if v, ok := message.Values["seq"].(string); ok {
					if seq, err := strconv.ParseUint(v, 10, 64); err == nil {
						event.Seq = seq
					}
				}
				if v, ok := message.Values["ts_nano"].(string); ok {
					if nano, err := strconv.ParseInt(v, 10, 64); err == nil {
						event.Timestamp = time.Unix(0, nano)
					}
				}
				// Payload 是 JSON 字符串，需要反序列化
				if v, ok := message.Values["payload"].(string); ok && v != "" {
					var p map[string]interface{}
					if err := json.Unmarshal([]byte(v), &p); err == nil {
						event.Payload = p
					}
				}

				// 从 Redis Stream 读取到的事件（可能由 gateway 等外部组件写入）
				// 也需要持久化到 PostgreSQL（DB 有唯一索引防重复）
				if shouldPersistEvent(event.Type) {
					el := db.EventLog{
						WorkflowID: event.WorkflowID,
						Type:       event.Type,
						AgentID:    event.AgentID,
						Message:    sanitizeEventMessage(event.Message),
						Timestamp:  event.Timestamp,
						Seq:        event.Seq,
						StreamID:   event.StreamID,
					}
					if event.Payload != nil {
						el.Payload = db.JSONB(sanitizeEventPayload(event.Payload))
					}
					m.enqueuePersistEvent(el)
				}

				// 将事件推送到 subscriber channel
				// 使用 select + default 实现非阻塞发送：
				//   - 如果通道未满，正常发送
				//   - 如果通道已满，丢弃事件（并记录告警日志）
				//   这样设计是为了防止慢消费者阻塞整个事件流
				select {
				case ch <- event:
					m.logger.Debug("Sent event to subscriber",
						zap.String("workflow_id", workflowID),
						zap.String("type", event.Type),
						zap.Uint64("seq", event.Seq),
						zap.String("stream_id", message.ID))
				default:
					if isCriticalEvent(event.Type) {
						m.logger.Error("CRITICAL: Dropped important event - subscriber slow",
							zap.String("workflow_id", workflowID),
							zap.String("type", event.Type),
							zap.Uint64("seq", event.Seq))
					} else {
						m.logger.Warn("Dropped event - subscriber slow",
							zap.String("workflow_id", workflowID),
							zap.String("type", event.Type),
							zap.Uint64("seq", event.Seq))
					}
				}
			}
		}
	}
}

// min 返回两个 time.Duration 中较小的一个。
// Go 标准库 math.Min 不支持 time.Duration（本质上是 int64），
// 所以需要这个辅助函数。
func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// isCriticalEvent 判断事件类型是否为关键事件。
// 关键事件在丢弃时会记录 Error 级别日志（而非 Warn），以引起运维注意。
//
// 关键事件定义：
//   - 工作流失败：可能导致业务中断
//   - 工作流完成：终端状态事件
//   - Agent 失败：子任务执行失败
//   - 错误发生：系统错误
//   - 工具错误：外部工具调用失败
func isCriticalEvent(eventType string) bool {
	switch eventType {
	case "WORKFLOW_FAILED",
		"WORKFLOW_COMPLETED",
		"AGENT_FAILED",
		"ERROR_OCCURRED",
		"TOOL_ERROR":
		return true
	default:
		return false
	}
}

// Unsubscribe 取消一个订阅，停止其 reader goroutine 并清理资源。
//
// 操作步骤：
//   1. 从 subscribers map 中找到对应的 subscription
//   2. 调用 cancel() —— 这会使 reader goroutine 的 context 变为取消状态
//   3. reader goroutine 检测到 ctx.Done() 后退出，defer close(ch) 关闭 channel
//   4. 从 subscribers map 中删除此订阅
//   5. 如果某个 workflowID 下没有其他订阅者，清理外层 map 条目
//
// 线程安全：通过 m.mu.Lock() 保护 map 操作。
// 幂等性：多次调用 Unsubscribe 对于同一个 channel 是安全的（第二次不会 panic）。
func (m *Manager) Unsubscribe(workflowID string, ch chan Event) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if subs, ok := m.subscribers[workflowID]; ok {
		if sub, exists := subs[ch]; exists {
			sub.cancel()
			delete(subs, ch)

			if len(subs) == 0 {
				delete(m.subscribers, workflowID)
			}
		}
	}
}

// ---------------------------------------------------------------------------
//  事件发布
// ---------------------------------------------------------------------------

// Publish 发布一个事件到整个系统。
//
// 三步分发策略：
//   第一步：Redis Stream —— 跨进程通信
//     写入 workflow 专属的事件流（近似裁剪到 capacity 长度）
//     写入后设置 24h TTL（Redis 自动清理过期数据）
//     对于可通知事件，额外写入全局通知流（webhook 投递用）
//     为 seqKey 设置 48h TTL（比 stream 长一倍，防止序列号丢失）
//
//   第二步：PostgreSQL（异步）
//     对于需要持久化的事件类型，异步加入持久化队列
//     由 persistWorker 批量写入 DB
//
//   第三步：本地 Subscriber（仅在 Redis 不可用时的降级模式）
//     当 m.redis == nil 时，直接写入本地 subscriber channel
//     当 Redis 可用时，事件由 streamReaderFrom 分发到 subscriber（保证所有进程一致）
//
// 序列号管理：
//   使用 Redis INCR 命令生成单调递增序列号（进程安全的计数器）
//   序列号用于：
//     - 去重（结合 workflow_id + type + seq 唯一索引）
//     - 排序（客户端按 seq 排序保证顺序）
//     - 断线重连时的位置标记
//
// 注意：
//   此方法不应在工作流代码中频繁调用（如每个 token 粒度）。
//   LLM_PARTIAL 等高频事件仅走 Redis，不进入 PostgreSQL 持久化。
func (m *Manager) Publish(workflowID string, evt Event) {
	if m.redis != nil {
		ctx := context.Background()

		// 第一步：原子自增序列号（Redis INCR 是原子的）
		seq, err := m.redis.Incr(ctx, m.seqKey(workflowID)).Result()
		if err != nil {
			m.logger.Error("Failed to increment sequence",
				zap.String("workflow_id", workflowID),
				zap.Error(err))
			seq = 0
		}
		evt.Seq = uint64(seq)

		// 第二步：写入 Redis Stream
		// Payload 需要 JSON 序列化为字符串（Redis Stream 的 Values 是 map[string]interface{}）
		streamKey := m.streamKey(workflowID)
		var payloadJSON string
		if evt.Payload != nil {
			if b, err := json.Marshal(evt.Payload); err == nil {
				payloadJSON = string(b)
			}
		}
		streamID, err := m.redis.XAdd(ctx, &redis.XAddArgs{
			Stream: streamKey,
			MaxLen: int64(m.capacity), // 近似裁剪防止无限增长
			Approx: true,              // 近似模式：Redis 在方便时再裁剪，性能更好
			Values: map[string]interface{}{
				"workflow_id": evt.WorkflowID,
				"type":        evt.Type,
				"agent_id":    evt.AgentID,
				"message":     evt.Message,
				"payload":     payloadJSON,
				"ts_nano":     strconv.FormatInt(evt.Timestamp.UnixNano(), 10),
				"seq":         strconv.FormatUint(evt.Seq, 10),
			},
		}).Result()

		if err != nil {
			m.logger.Error("Failed to publish to Redis stream",
				zap.String("workflow_id", workflowID),
				zap.Error(err))
		} else {
			evt.StreamID = streamID
			m.logger.Debug("Published event to Redis stream",
				zap.String("workflow_id", workflowID),
				zap.String("type", evt.Type),
				zap.Uint64("seq", evt.Seq),
				zap.String("stream_id", streamID))
		}

		// 第三步：设置 TTL（Redis 自动过期清理）
		// Stream key 的 TTL 设为 24h（留存足够时间供回放/重连）
		// Seq key 的 TTL 设为 48h（避免序列号过早丢失导致新事件从头计数）
		m.redis.Expire(ctx, streamKey, 24*time.Hour)
		m.redis.Expire(ctx, m.seqKey(workflowID), 48*time.Hour)

		// 第四步：全局通知流（用于 webhook 投递）
		//  只有终端工作流事件才写入全局流，避免大量中间事件冲刷
		//  全局流被 webhook 投递服务消费
		if isNotifiableEvent(evt.Type) {
			globalKey := "shannon:notifications:global"
			_, gErr := m.redis.XAdd(ctx, &redis.XAddArgs{
				Stream: globalKey,
				MaxLen: globalNotificationMaxLen,
				Approx: true,
				Values: map[string]interface{}{
					"workflow_id": evt.WorkflowID,
					"type":        evt.Type,
					"agent_id":    evt.AgentID,
					"message":     evt.Message,
					"ts_nano":     strconv.FormatInt(evt.Timestamp.UnixNano(), 10),
				},
			}).Result()
			if gErr != nil {
				m.logger.Error("Failed to publish to global notification stream",
					zap.String("workflow_id", workflowID),
					zap.String("type", evt.Type),
					zap.Error(gErr))
			}
			m.redis.Expire(ctx, globalKey, 48*time.Hour)
		}
	}

	// 第五步：异步持久化到 PostgreSQL
	if shouldPersistEvent(evt.Type) {
		el := db.EventLog{
			WorkflowID: evt.WorkflowID,
			Type:       evt.Type,
			AgentID:    evt.AgentID,
			Message:    sanitizeUTF8(evt.Message),
			Timestamp:  evt.Timestamp,
			Seq:        evt.Seq,
			StreamID:   evt.StreamID,
		}
		if evt.Payload != nil {
			el.Payload = db.JSONB(sanitizePayloadForPersistence(evt.Type, evt.Payload))
		}
		m.enqueuePersistEvent(el)
	}

	// 第六步：本地分发（仅 Redis 不可用时的降级模式）
	// 当 Redis 可用时，事件由 streamReaderFrom 负责分发到 subscriber，
	// 这里不重复分发，避免双倍发送。
	if m.redis == nil {
		m.mu.RLock()
		defer m.mu.RUnlock()
		subs := m.subscribers[workflowID]
		if len(subs) == 0 {
			return
		}
		for ch := range subs {
			select {
			case ch <- evt:
			default:
				// 慢消费者丢弃
			}
		}
	}
}

// Marshal 将 Event 序列化为 JSON 字节，用于 SSE 发送和日志输出。
func (e Event) Marshal() []byte {
	b, _ := json.Marshal(e)
	return b
}

// ---------------------------------------------------------------------------
//  事件类型过滤函数
// ---------------------------------------------------------------------------

// shouldPersistEvent 判断事件类型是否需要持久化到 PostgreSQL。
//
// 设计原则：
//   - 高频流式事件（LLM_PARTIAL、HEARTBEAT）不持久化，避免写入压力
//   - 终端状态事件（COMPLETED、FAILED）必须持久化，用于审计和回放
//   - Agent 思考过程（AGENT_THINKING）持久化，保证时间线连续性
//   - 未知事件类型默认持久化（安全策略，宁可多存不少存）
//
// 持久化策略选择：
//   全量持久化 → PostgreSQL 存储和写入压力大
//   全不持久化 → 无法实现历史回放、审计追踪
//   选择性持久化 → 兼顾存储成本和功能需求（本文件采用此策略）
func shouldPersistEvent(eventType string) bool {
	switch eventType {
	// 需要持久化的事件 —— 状态变更、工具调用、错误
	case "WORKFLOW_COMPLETED",
		"WORKFLOW_FAILED",
		"AGENT_COMPLETED",
		"AGENT_FAILED",
		"TOOL_INVOKED",
		"TOOL_OBSERVATION",
		"TOOL_ERROR",
		"ERROR_OCCURRED",
		"LLM_OUTPUT",
		"STREAM_END",
		// 多 Agent 协调事件
		"ROLE_ASSIGNED",
		"DELEGATION",
		"BUDGET_THRESHOLD",
		"SCREENSHOT_SAVED":
		return true

	// 不持久化的事件 —— 高频流式增量
	case "LLM_PARTIAL", // LLM 输出流式增量（每秒可能数十次）
		"HEARTBEAT",     // 心跳检测（几秒一次，无实际信息）
		"PING",
		"LLM_PROMPT": // Prompt 内容单独记录，不在此处持久化
		return false

	case "AGENT_THINKING":
		// Agent 思考过程需要持久化，确保实时回放和历史快照的时间线一致
		return true

	// 默认持久化（安全策略）
	default:
		return true
	}
}

// isNotifiableEvent 判断事件类型是否需要触发 Webhook 通知。
// 只有工作流的终端事件（完成/失败）才触发通知，避免中间状态频繁推送。
func isNotifiableEvent(eventType string) bool {
	switch eventType {
	case "WORKFLOW_COMPLETED", "WORKFLOW_FAILED":
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
//  数据清洗与安全
// ---------------------------------------------------------------------------

// SanitizeBase64Image 截断消息中过大的 Base64 编码图片数据。
//
// 为什么需要截断：
//   - 浏览器工具产生的截图 base64 可能达到数 MB
//   - 存入 PostgreSQL 会大幅增加存储成本和查询延迟
//   - 在日志中会淹没有用信息
//
// 工作原理：
//   1. 遍历已知的 base64 图片前缀模式
//   2. 匹配到后将 base64 数据替换为占位符 "[BASE64_IMAGE_TRUNCATED]"
//   3. 仅截断超过 1KB 的 base64 数据
//
// 注意：此函数是幂等的 —— 多次调用不会导致错误。
func SanitizeBase64Image(s string) string {
	if s == "" {
		return s
	}

	patterns := []string{
		`"screenshot": "data:image/`,
		`"screenshot":"data:image/`,
		`"image": "data:image/`,
		`"image":"data:image/`,
		`"base64": "`,
		`"base64":"`,
	}

	result := s
	for _, pattern := range patterns {
		for {
			idx := strings.Index(result, pattern)
			if idx == -1 {
				break
			}

			startIdx := idx + len(pattern)
			if startIdx >= len(result) {
				break
			}

			endIdx := strings.Index(result[startIdx:], `"`)
			if endIdx == -1 {
				break
			}

			dataLen := endIdx
			if dataLen > 1024 {
				placeholder := "[BASE64_IMAGE_TRUNCATED]"
				result = result[:startIdx] + placeholder + result[startIdx+endIdx:]
			} else {
				break
			}
		}
	}

	return result
}

// sanitizeEventMessage 清洗事件消息：先清理非法 UTF-8，再截断 base64 图片。
func sanitizeEventMessage(s string) string {
	s = sanitizeUTF8(s)
	s = SanitizeBase64Image(s)
	return s
}

// sanitizeEventPayload 递归清洗事件的 Payload 字段。
//
// 递归深度限制：最大 4 层，防止深度嵌套对象导致栈溢出或无限递归。
//
// 处理规则：
//   - 字符串：检查 key 名，如果是 screenshot/popup_screenshot 且长度 > 1KB 则截断
//   - Map：递归清洗每个值
//   - 数组：递归清洗每个元素
//   - 其他类型：保持不变
func sanitizeEventPayload(payload map[string]interface{}) map[string]interface{} {
	if payload == nil {
		return nil
	}

	const maxDepth = 4
	var sanitizeValue func(key string, v interface{}, depth int) interface{}
	sanitizeValue = func(key string, v interface{}, depth int) interface{} {
		if v == nil || depth > maxDepth {
			return v
		}

		switch val := v.(type) {
		case string:
			if (key == "screenshot" || key == "popup_screenshot") && len(val) > 1024 {
				return "[BASE64_IMAGE_TRUNCATED]"
			}
			return SanitizeBase64Image(val)
		case map[string]interface{}:
			return sanitizeEventPayload(val)
		case []interface{}:
			out := make([]interface{}, 0, len(val))
			for _, item := range val {
				out = append(out, sanitizeValue("", item, depth+1))
			}
			return out
		default:
			return v
		}
	}

	sanitized := make(map[string]interface{}, len(payload))
	for k, v := range payload {
		sanitized[k] = sanitizeValue(k, v, 0)
	}
	return sanitized
}

// sanitizePayloadForPersistence 专门针对持久化的 Payload 清洗。
//
// 与 sanitizeEventPayload 的区别：
//   - 应用于持久化场景（PostgreSQL）
//   - 针对 TOOL_OBSERVATION + browser 工具的场景截图做特殊处理
//   - 保留其他 Payload 完整不动（Redis/SSE 中仍然是完整数据）
//
// 为什么 Redis 保留完整数据但 PostgreSQL 要截断？
//   Redis 做实时推送，前端需要完整截图用于展示
//   PostgreSQL 做长期存储，base64 截图占据空间且很少被查询
//   查询历史时只需要知道"这里有截图"而不需要图片内容
func sanitizePayloadForPersistence(eventType string, payload map[string]interface{}) map[string]interface{} {
	if payload == nil {
		return nil
	}

	if eventType != "TOOL_OBSERVATION" {
		return payload
	}

	tool, hasT := payload["tool"].(string)
	output, hasO := payload["output"].(map[string]interface{})
	if !hasT || !hasO || tool != "browser" {
		return payload
	}
	if _, hasScreenshot := output["screenshot"]; !hasScreenshot {
		return payload
	}

	sanitized := make(map[string]interface{})
	for k, v := range payload {
		if k == "output" {
			sanitizedOutput := make(map[string]interface{})
			for ok, ov := range output {
				if ok == "screenshot" {
					sanitizedOutput[ok] = "[BASE64_STRIPPED_FOR_PERSISTENCE]"
				} else {
					sanitizedOutput[ok] = ov
				}
			}
			sanitized[k] = sanitizedOutput
		} else {
			sanitized[k] = v
		}
	}
	return sanitized
}

// ---------------------------------------------------------------------------
//  事件持久化（异步批量写入 PostgreSQL）
// ---------------------------------------------------------------------------

// enqueuePersistEvent 将事件加入持久化队列（非阻塞）。
//
// 设计考虑：
//   不能在 Publish() 或 streamReaderFrom 中同步写入 PostgreSQL，
//   因为 DB 写入是磁盘 I/O 操作，可能阻塞几十毫秒。
//   流式事件可能每秒产生数百条，同步写入会严重拖慢事件分发。
//
// 解决方案：异步批量写入
//   Publish() → 将事件写入 persistCh（带缓冲的通道）
//   persistWorker → 批量读取并写入 PostgreSQL
//
// 关闭安全性：
//   使用 persistMu 保证关闭和写入互斥。
//   Shutdown() 先锁 persistMu，置 persistClosed 标志，再关闭 persistCh。
//   这里先锁 persistMu，检查 persistClosed 标志后再尝试写通道。
//
// 通道满时：
//   非关键事件 → Warn 级别日志（可接受丢事件）
//   关键事件   → Error 级别日志（需要运维关注）
func (m *Manager) enqueuePersistEvent(event db.EventLog) {
	m.persistMu.Lock()
	defer m.persistMu.Unlock()

	if m.dbClient == nil || m.persistCh == nil || m.persistClosed {
		return
	}

	select {
	case m.persistCh <- event:
	default:
		if isCriticalEvent(event.Type) {
			m.logger.Error("CRITICAL: eventlog batcher full; dropping important event",
				zap.String("workflow_id", event.WorkflowID),
				zap.String("type", event.Type))
		} else {
			m.logger.Warn("eventlog batcher full; dropping event",
				zap.String("workflow_id", event.WorkflowID),
				zap.String("type", event.Type))
		}
	}
}

// sanitizeUTF8 清理字符串中的非法 UTF-8 字节序列。
//
// 必要原因：
//   PostgreSQL 拒绝包含非法 UTF-8 的数据（error: invalid byte sequence for encoding "UTF8"）
//   LLM 输出偶尔包含非法 UTF-8 字节，需要过滤后才能写入数据库。
//
// 实现方式：
//   逐字节检查转换，遇到非法字节（RuneError 且占用 1 字节）时跳过。
//   合法字符正常保留。
func sanitizeUTF8(s string) string {
	if s == "" || utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			s = s[size:]
			continue
		}
		b.WriteRune(r)
		s = s[size:]
	}
	return b.String()
}

// persistWorker —— 事件持久化的异步批量写入协程。
//
// 设计模式：定时批量刷入（time-based + count-based)
//   - count-based：每积累到 batchSize 条就刷入一次
//   - time-based：每个 flushEvery 间隔刷入一次（防止低流量时事件长期滞留）
//
// 这两个条件"谁先触发谁刷入"，确保：
//   - 高流量时：积累到 batchSize 就刷，减少延迟
//   - 低流量时：最多等 flushEvery 时间，保证数据及时落盘
//
// 写入策略：串行逐条写入（当前实现）。
// 虽然可以优化为批量 INSERT，但串行写入更简单安全，且 error handling 更精确。
// 如果未来写入成为瓶颈，可以改为 batch insert（使用 pgx.CopyFrom 或批量 VALUES）。
//
// 关闭流程：
//   1. Shutdown() 关闭 persistCh
//   2. for-range 读取到 !ok，执行最后一次 flush()
//   3. 退出，waitgroup 计数器减一
func (m *Manager) persistWorker() {
	defer m.persistWg.Done()
	batch := make([]db.EventLog, 0, m.batchSize)
	ticker := time.NewTicker(m.flushEvery)
	defer ticker.Stop()
	flush := func() {
		if len(batch) == 0 || m.dbClient == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		for i := range batch {
			if err := m.dbClient.SaveEventLog(ctx, &batch[i]); err != nil {
				m.logger.Warn("SaveEventLog failed", zap.String("workflow_id", batch[i].WorkflowID), zap.String("type", batch[i].Type), zap.Uint64("seq", batch[i].Seq), zap.Error(err))
			}
		}
		cancel()
		batch = batch[:0]
	}
	for {
		select {
		case ev, ok := <-m.persistCh:
			if !ok {
				flush()
				return
			}
			batch = append(batch, ev)
			if len(batch) >= m.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// ---------------------------------------------------------------------------
//  事件回放
// ---------------------------------------------------------------------------

// ReplaySince 从 Redis Stream 中回放指定工作流中序列号大于 since 的事件。
//
// 用途：
//   前端断线重连时，传入客户端已收到的最后一条事件的序列号，
//   服务器返回所有新事件用于补全。
//
// 实现方式：
//   使用 Redis XRange 命令遍历全量 Stream（从 "-" 到 "+"）。
//   遍历时按 seq 过滤，只返回 seq > since 的事件。
//
// 性能注意：
//   此函数会读取 Redis Stream 中该工作流的所有消息。
//   如果某个工作流产生了大量事件（如长时间运行的 Agent），
//   此调用可能较慢。Stream 设置了 MaxLen 防止无限增长。
//
// 返回值：
//   符合条件的 Event 列表。如果没有 Redis 则返回 nil。
func (m *Manager) ReplaySince(workflowID string, since uint64) []Event {
	if m.redis == nil {
		return nil
	}

	ctx := context.Background()
	streamKey := m.streamKey(workflowID)

	messages, err := m.redis.XRange(ctx, streamKey, "-", "+").Result()
	if err != nil {
		m.logger.Error("Failed to read replay from Redis stream",
			zap.String("workflow_id", workflowID),
			zap.Error(err))
		return nil
	}

	var events []Event
	for _, msg := range messages {
		event := Event{
			WorkflowID: workflowID,
			StreamID:   msg.ID,
		}

		if v, ok := msg.Values["seq"].(string); ok {
			if seq, err := strconv.ParseUint(v, 10, 64); err == nil {
				event.Seq = seq
				if seq <= since {
					continue
				}
			}
		}

		if v, ok := msg.Values["type"].(string); ok {
			event.Type = v
		}
		if v, ok := msg.Values["agent_id"].(string); ok {
			event.AgentID = v
		}
		if v, ok := msg.Values["message"].(string); ok {
			event.Message = v
		}
		if v, ok := msg.Values["ts_nano"].(string); ok {
			if nano, err := strconv.ParseInt(v, 10, 64); err == nil {
				event.Timestamp = time.Unix(0, nano)
			}
		}
		if v, ok := msg.Values["payload"].(string); ok && v != "" {
			var p map[string]interface{}
			if err := json.Unmarshal([]byte(v), &p); err == nil {
				event.Payload = p
			}
		}

		events = append(events, event)
	}

	return events
}

// ReplayFromStreamID 从指定的 Redis Stream ID 开始回放事件。
//
// 与 ReplaySince 的区别：
//   ReplaySince 使用业务序列号（seq）过滤 —— 适用于应用级别的断线重连
//   ReplayFromStreamID 使用 Redis Stream ID 过滤 —— 适用于精确的流位置恢复
//
// Redis Stream ID 格式：<millisecondsTime>-<sequenceNumber>
// XRange 中的 "(" + streamID 表示"排他性范围"（不包含 streamID 本身）。
//
// 使用场景：
//   当订阅者已经消费到某个 Stream ID 后断线，
//   调用此函数可以精确恢复断点后的所有事件。
func (m *Manager) ReplayFromStreamID(workflowID string, streamID string) []Event {
	if m.redis == nil {
		return nil
	}

	ctx := context.Background()
	streamKey := m.streamKey(workflowID)

	messages, err := m.redis.XRange(ctx, streamKey, "("+streamID, "+").Result()
	if err != nil {
		m.logger.Error("Failed to read replay from Redis stream",
			zap.String("workflow_id", workflowID),
			zap.String("stream_id", streamID),
			zap.Error(err))
		return nil
	}

	var events []Event
	for _, msg := range messages {
		event := Event{
			WorkflowID: workflowID,
			StreamID:   msg.ID,
		}

		if v, ok := msg.Values["seq"].(string); ok {
			if seq, err := strconv.ParseUint(v, 10, 64); err == nil {
				event.Seq = seq
			}
		}
		if v, ok := msg.Values["type"].(string); ok {
			event.Type = v
		}
		if v, ok := msg.Values["agent_id"].(string); ok {
			event.AgentID = v
		}
		if v, ok := msg.Values["message"].(string); ok {
			event.Message = v
		}
		if v, ok := msg.Values["ts_nano"].(string); ok {
			if nano, err := strconv.ParseInt(v, 10, 64); err == nil {
				event.Timestamp = time.Unix(0, nano)
			}
		}
		if v, ok := msg.Values["payload"].(string); ok && v != "" {
			var p map[string]interface{}
			if err := json.Unmarshal([]byte(v), &p); err == nil {
				event.Payload = p
			}
		}

		events = append(events, event)
	}

	return events
}

// HasEmittedCompletion 检查工作流是否已经发出了 WORKFLOW_COMPLETED 事件。
//
// 用途：
//   解决 Temporal 工作流状态与事件流之间的可见性竞态问题。
//   Temporal 可能在工作流完成后才将状态写入数据库，
//   但事件流（Redis Stream）可能已经先收到了 COMPLETED 事件。
//   此函数给调用方一个"快速检查"的手段。
//
// 实现方式：
//   通过 XRevRangeN 反向扫描 Stream 尾部（最多 10 条消息）。
//   反向扫描 + 限制条数 = O(1) 操作，不随 Stream 增长而变慢。
//
// 超时处理：
//   设置 100ms 超时，避免 Redis 慢查询阻塞关键路径。
//   超时或错误时返回 false（保守策略：宁可说没完成）。
func (m *Manager) HasEmittedCompletion(ctx context.Context, workflowID string) bool {
	if m.redis == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}

	checkCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	streamKey := m.streamKey(workflowID)

	const scanCount int64 = 10
	messages, err := m.redis.XRevRangeN(checkCtx, streamKey, "+", "-", scanCount).Result()
	if err != nil {
		if err == redis.Nil || checkCtx.Err() != nil {
			return false
		}
		m.logger.Debug("Failed to check completion status in Redis",
			zap.String("workflow_id", workflowID),
			zap.Error(err))
		return false
	}

	for _, msg := range messages {
		if eventType, ok := msg.Values["type"].(string); ok && eventType == "WORKFLOW_COMPLETED" {
			return true
		}
	}

	return false
}

// GetLastStreamID 获取工作流事件流中最后一条消息的 Redis Stream ID。
//
// 用途：
//   客户端断线重连时，记录下当前最后的 Stream ID，
//   然后调用 SubscribeFrom(workflowID, buffer, lastID) 从断点继续。
//
// 实现方式：
//   使用 XRevRangeN 反向扫描，只取 1 条（最新消息）。
//   空流返回空字符串。
func (m *Manager) GetLastStreamID(workflowID string) string {
	if m.redis == nil {
		return ""
	}

	ctx := context.Background()
	streamKey := m.streamKey(workflowID)

	messages, err := m.redis.XRevRangeN(ctx, streamKey, "+", "-", 1).Result()
	if err != nil || len(messages) == 0 {
		return ""
	}

	return messages[0].ID
}

// ---------------------------------------------------------------------------
//  优雅关闭
// ---------------------------------------------------------------------------

// Shutdown 优雅关闭 Streaming Manager。
//
// 关闭顺序（层次化关闭，防止数据丢失）：
//   第一阶段：停止事件消费
//     1. close(shutdownCh) —— 通知所有 streamReaderFrom goroutine 退出
//     2. 取消所有订阅（遍历 subscribers，逐个调用 cancel）
//     3. 等待所有 streamReaderFrom 退出（m.wg.Wait()）
//
//   第二阶段：持久化刷入
//     1. 标记 persistCh 为已关闭（persistClosed = true）
//     2. 关闭 persistCh
//     3. persistWorker 收到关闭信号后执行最后一次 flush()
//     4. 等待 persistWorker 退出
//
// 超时处理：
//   ctx 参数控制整体超时。
//   如果第一阶段超时，可能仍有 streamReaderFrom 未退出（可能阻塞在 XRead 上）
//   如果第二阶段超时，可能最后一批事件未写入 DB（业务可接受的数据丢失）
//
// 返回值：
//   nil —— 正常关闭
//   ctx.Err() —— 关闭超时
func (m *Manager) Shutdown(ctx context.Context) error {
	m.logger.Info("Shutting down streaming manager")

	// 第一阶段：发送全局关闭信号
	close(m.shutdownCh)

	// 取消所有订阅（避免个别 goroutine 未响应 shutdownCh）
	m.mu.Lock()
	for workflowID, subs := range m.subscribers {
		for ch, sub := range subs {
			sub.cancel()
			delete(subs, ch)
		}
		delete(m.subscribers, workflowID)
	}
	m.mu.Unlock()

	// 等待所有 stream reader 退出
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		m.logger.Info("All stream readers stopped")
	case <-ctx.Done():
		m.logger.Warn("Shutdown timeout waiting for stream readers")
		return ctx.Err()
	}

	// 第二阶段：持久化刷入
	if m.persistCh != nil {
		m.persistMu.Lock()
		if !m.persistClosed {
			m.persistClosed = true
			close(m.persistCh)
		}
		m.persistMu.Unlock()

		persistDone := make(chan struct{})
		go func() {
			m.persistWg.Wait()
			close(persistDone)
		}()

		select {
		case <-persistDone:
			m.logger.Info("Event persistence flushed")
		case <-ctx.Done():
			m.logger.Warn("Shutdown timeout waiting for persistence flush")
			return ctx.Err()
		}
	}

	m.logger.Info("Streaming manager shutdown complete")
	return nil
}

// ---------------------------------------------------------------------------
//  大对象（Blob）存储 —— 解决 Temporal 对事件大小的限制
// ---------------------------------------------------------------------------

// 背景：
//   Temporal 对工作流事件有 256KB 的大小限制（默认配置）。
//   浏览器工具的截图（base64）可能达到数 MB，无法通过 Temporal 事件传递。
//   解决方案：将大对象（blob）存储到 Redis，只传递 Redis key 引用。
//
// 使用场景：
//   - 浏览器截图（Browser Use 工具)
//   - 大型文件内容
//   - 任何超过 Temporal 限制的数据

const (
	// blobKeyPrefix —— Redis key 前缀，用于隔离 blob 和其他数据
	blobKeyPrefix = "shannon:blob:"
	// blobTTL —— blob 在 Redis 中的过期时间（7 天）
	blobTTL = 7 * 24 * time.Hour
)

// StoreBlob 将大对象存储到 Redis 并返回可引用的 key。
//
// 参数：
//   ctx       —— 上下文
//   workflowID —— 所属工作流（用于构建 key）
//   fieldName —— 字段名（如 "screenshot"），与 workflowID 一起构成唯一 key
//   data      —— blob 数据（base64 编码的图片等）
//
// 返回值：
//   key —— 可用于后续 GetBlob 检索的 Redis key
//   err —— 存储失败时的错误
//
// 调用约定：
//   工作流代码中，当遇到大型数据时调用 StoreBlob 存储，
//   然后将返回的 key 放入 Event.Payload 中传递。
//   消费端（前端）通过 key 调用 GetBlob 获取完整数据。
func (m *Manager) StoreBlob(ctx context.Context, workflowID, fieldName, data string) (string, error) {
	if m.redis == nil {
		return "", fmt.Errorf("redis not configured")
	}

	key := fmt.Sprintf("%s%s:%s", blobKeyPrefix, workflowID, fieldName)

	err := m.redis.Set(ctx, key, data, blobTTL).Err()
	if err != nil {
		m.logger.Error("Failed to store blob in Redis",
			zap.String("key", key),
			zap.Int("size", len(data)),
			zap.Error(err))
		return "", err
	}

	m.logger.Debug("Stored blob in Redis",
		zap.String("key", key),
		zap.Int("size", len(data)),
		zap.Duration("ttl", blobTTL))

	return key, nil
}

// GetBlob 根据 key 从 Redis 检索 blob 数据。
//
// 如果 key 不存在或已过期，返回 ("", nil)（非错误情况）。
// 如果 Redis 访问出错，返回 ("", err)。
func (m *Manager) GetBlob(ctx context.Context, key string) (string, error) {
	if m.redis == nil {
		return "", fmt.Errorf("redis not configured")
	}

	data, err := m.redis.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		m.logger.Error("Failed to get blob from Redis",
			zap.String("key", key),
			zap.Error(err))
		return "", err
	}

	return data, nil
}

// RefreshBlobTTL 延长 blob 的过期时间（续期）。
//
// 用途：
//   当某个 blob 仍在被前端访问时，定期调用此函数续期，
//   防止在长时间运行的工作流中 blob 被 Redis 自动清理。
//
// 每次调用将过期时间重置为 blobTTL（7 天）。
func (m *Manager) RefreshBlobTTL(ctx context.Context, key string) error {
	if m.redis == nil {
		return fmt.Errorf("redis not configured")
	}

	return m.redis.Expire(ctx, key, blobTTL).Err()
}
