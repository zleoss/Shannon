"""=============================================================================
文件: python/llm-service/llm_service/tools/base.py
-------------------------------------------------------------------------------
【一句话功能】
  Shannon 工具抽象基类：参数、元数据、执行结果与通用执行框架。
-------------------------------------------------------------------------------
【说明】
  该模块定义了 Shannon 工具系统的核心类型和行为。
  - ToolParameterType: 工具参数类型枚举。
  - ToolParameter: 单个参数定义。
  - ToolMetadata: 工具元数据。
  - ToolResult: 工具执行结果。
  - Tool: 工具抽象基类，包含参数校验、类型转换、限流与执行生命周期。
-------------------------------------------------------------------------------
"""

from abc import ABC, abstractmethod
import asyncio
from dataclasses import dataclass
from datetime import datetime, timedelta
from enum import Enum
from typing import Any, Dict, List, Optional, Union
import json
import time
import uuid

# Rate limiting constants
RATE_LIMIT_HIGH_THROUGHPUT_THRESHOLD = 60  # req/min - skip per-session tracking above this
RATE_LIMIT_SKIP_THRESHOLD = 100  # req/min - skip rate limiting entirely above this
TRACKER_MAX_ENTRIES = 100  # Maximum entries in execution tracker before cleanup


class ToolParameterType(Enum):
    """Supported parameter types for tools.

    该枚举用于描述工具参数的数据类型，后续会用于参数校验和 JSON schema 生成。
    """

    STRING = "string"
    INTEGER = "integer"
    FLOAT = "float"
    BOOLEAN = "boolean"
    ARRAY = "array"
    OBJECT = "object"
    FILE = "file"  # For file paths or file content


@dataclass
class ToolParameter:
    """Definition of a tool parameter.

    每个工具参数都有名字、类型和描述，可以额外指定是否必填、默认值、枚举取值、取值范围等。
    """

    name: str
    type: ToolParameterType
    description: str
    required: bool = True
    default: Any = None
    enum: Optional[List[Any]] = None  # For enumerated values
    min_value: Optional[Union[int, float]] = None
    max_value: Optional[Union[int, float]] = None
    pattern: Optional[str] = None  # Regex pattern for validation
    items: Optional[Dict[str, Any]] = None  # For ARRAY types: {"type": "string"}


@dataclass
class ToolMetadata:
    """Metadata about a tool.

    工具元数据用于描述工具能力、分类、授权需求、限流与运行预算等信息。
    """

    name: str
    version: str
    description: str
    category: str  # e.g., "search", "calculation", "file", "database"
    author: str = "Shannon"
    requires_auth: bool = False
    rate_limit: Optional[int] = None  # Requests per minute
    timeout_seconds: int = 30
    memory_limit_mb: int = 512
    sandboxed: bool = True  # Whether to run in sandbox
    session_aware: bool = True  # Whether tool uses session context (default True to avoid footgun)
    dangerous: bool = False  # Requires extra confirmation
    cost_per_use: float = 0.0  # Cost in USD per invocation
    input_examples: Optional[List[Dict[str, Any]]] = None  # Examples for tool usage (Anthropic-specific)


@dataclass
class ToolResult:
    """Result from tool execution.

    所有工具执行都应返回此结构，方便统一序列化、日志和错误处理。
    """

    success: bool
    output: Any
    error: Optional[str] = None
    metadata: Optional[Dict[str, Any]] = None
    execution_time_ms: Optional[int] = None
    tokens_used: Optional[int] = None
    cost_usd: Optional[float] = None
    cost_model: Optional[str] = None  # synthetic model name for cost attribution

    def to_dict(self) -> Dict[str, Any]:
        """Convert to dictionary for serialization."""
        d = {
            "success": self.success,
            "output": self.output,
            "error": self.error,
            "metadata": self.metadata or {},
            "execution_time_ms": self.execution_time_ms,
            "tokens_used": self.tokens_used,
        }
        if self.cost_usd is not None:
            d["cost_usd"] = self.cost_usd
        if self.cost_model is not None:
            d["cost_model"] = self.cost_model
        return d

    def to_json(self) -> str:
        """Convert to JSON string."""
        return json.dumps(self.to_dict())


class Tool(ABC):
    """Abstract base class for all tools.

    这个基类封装了工具执行前后的公共逻辑，包括参数转换、验证、限流和结果处理。
    具体工具只需实现元数据、参数定义和具体执行逻辑。
    """

    def __init__(self):
        self.metadata = self._get_metadata()
        self.parameters = self._get_parameters()
        self._execution_count = 0
        # Track rate limits per session/user instead of globally.
        self._execution_tracker: Dict[str, datetime] = {}
        # Request-scoped ID for rate limiting when no session/agent context.
        self._current_request_id: Optional[str] = None

    @abstractmethod
    def _get_metadata(self) -> ToolMetadata:
        """Return tool metadata."""
        pass

    @abstractmethod
    def _get_parameters(self) -> List[ToolParameter]:
        """Return list of tool parameters."""
        pass

    @abstractmethod
    async def _execute_impl(
        self, session_context: Optional[Dict] = None, **kwargs
    ) -> ToolResult:
        """Actual tool execution implementation.

        子类必须实现该方法。kwargs 包含所有工具参数。
        session_context 在工具声明为 session_aware 时会传入，否则为 None。
        """
        pass

    async def execute(
        self,
        session_context: Optional[Dict] = None,
        observer: Optional[Any] = None,
        **kwargs,
    ) -> ToolResult:
        """Execute the tool with validation, rate limiting and error handling."""
        # 记录执行开始时间，用于计算总耗时。
        start_time = time.time()

        # 每次执行前重置 request_id，确保无 session/agent 时不会复用先前请求的跟踪键。
        self._reset_request_id()

        try:
            # 参数预处理：先尝试按期望类型进行转换，后续再严格校验。
            kwargs = self._coerce_parameters(kwargs)
            self._validate_parameters(kwargs)

            # 尝试从上下文中读取 session_id 和 agent_id，用于限流追踪。
            session_id = None
            agent_id = None
            if session_context and isinstance(session_context, dict):
                session_id = session_context.get("session_id")
                agent_id = session_context.get("agent_id")

            # 生成限流 tracker key：优先 session、再 agent、最后 request。
            tracker_key = self._get_tracker_key(session_id, agent_id)

            # 如果本工具配置了限流且该限流值低于跳过阈值，则检查是否需要等待。
            if self.metadata.rate_limit and self.metadata.rate_limit < RATE_LIMIT_SKIP_THRESHOLD:
                retry_after = self._get_retry_after(tracker_key)
                if retry_after is not None:
                    # 有剩余等待时间时，自动 sleep，避免主动抛错并让调用方重试。
                    await asyncio.sleep(min(retry_after, 30))

            # 根据工具是否依赖 session 上下文，决定是否传入 session_context。
            if self.metadata.session_aware:
                result = await self._execute_impl(
                    session_context=session_context, observer=observer, **kwargs
                )
            else:
                result = await self._execute_impl(
                    session_context=None, observer=observer, **kwargs
                )

            # 每次实际调用计数。
            self._execution_count += 1

            # 只有 session/agent 级别的 key 才记录到限流跟踪表中。
            if not tracker_key.startswith("request:"):
                self._execution_tracker[tracker_key] = datetime.now()

                # 当跟踪表超过最大条目时，清理最旧的记录。
                if len(self._execution_tracker) > TRACKER_MAX_ENTRIES:
                    sorted_keys = sorted(
                        self._execution_tracker.items(), key=lambda x: x[1]
                    )
                    for key, _ in sorted_keys[: len(sorted_keys) - TRACKER_MAX_ENTRIES]:
                        del self._execution_tracker[key]

            # 计算总耗时，并写入结果结构。
            execution_time = int((time.time() - start_time) * 1000)
            result.execution_time_ms = execution_time

            return result

        except Exception as e:
            # 捕获所有异常，转换为统一的失败结果返回给调用方。
            return ToolResult(
                success=False,
                output=None,
                error=str(e),
                execution_time_ms=int((time.time() - start_time) * 1000),
            )

    def _coerce_parameters(self, kwargs: Dict[str, Any]) -> Dict[str, Any]:
        """Convert input values to expected parameter types when possible."""
        if not kwargs:
            return {}

        # 复制一份参数，避免修改原始输入字典。
        out = dict(kwargs)
        spec = {p.name: p for p in self.parameters}

        for name, param in spec.items():
            if name not in out:
                continue

            val = out[name]
            try:
                if param.type == ToolParameterType.INTEGER:
                    # 支持 float->int 转换，例如 3.0 转成 3。
                    if isinstance(val, float) and float(val).is_integer():
                        out[name] = int(val)
                    elif isinstance(val, str):
                        # 支持字符串数字，如 "42"。
                        s = val.strip()
                        if s.isdigit() or (s.startswith("-") and s[1:].isdigit()):
                            out[name] = int(s)

                    # 如果转换成功，再应用范围约束以避免后续校验失败。
                    if isinstance(out[name], int):
                        if param.max_value is not None and out[name] > param.max_value:
                            out[name] = param.max_value
                        if param.min_value is not None and out[name] < param.min_value:
                            out[name] = param.min_value

                elif param.type == ToolParameterType.FLOAT:
                    # int -> float 的隐式转换。
                    if isinstance(val, int):
                        out[name] = float(val)
                    elif isinstance(val, str):
                        # 字符串数字转换，例如 "3.14"。
                        s = val.strip()
                        out[name] = float(s)

                    if isinstance(out[name], float):
                        if param.max_value is not None and out[name] > param.max_value:
                            out[name] = float(param.max_value)
                        if param.min_value is not None and out[name] < param.min_value:
                            out[name] = float(param.min_value)

                elif param.type == ToolParameterType.BOOLEAN:
                    # 允许字符串形式的布尔值。
                    if isinstance(val, str):
                        s = val.strip().lower()
                        if s in ("true", "1", "yes", "y"):
                            out[name] = True
                        elif s in ("false", "0", "no", "n"):
                            out[name] = False
            except Exception:
                # 任何转换异常都不影响原参数，后续严格校验会报告问题。
                pass

        return out

    def _validate_parameters(self, kwargs: Dict[str, Any]) -> None:
        """Validate input parameters against tool definitions."""
        for param in self.parameters:
            # 必填参数必须存在于 kwargs 中。
            if param.required and param.name not in kwargs:
                raise ValueError(f"Required parameter '{param.name}' is missing")

            if param.name in kwargs:
                value = kwargs[param.name]

                # 检查值类型是否匹配预期类型。
                if not self._validate_type(value, param.type):
                    raise TypeError(
                        f"Parameter '{param.name}' expects type {param.type.value}, "
                        f"got {type(value).__name__}"
                    )

                # 如果声明了枚举值，必须严格命中某一项。
                if param.enum and value not in param.enum:
                    raise ValueError(
                        f"Parameter '{param.name}' must be one of {param.enum}"
                    )

                # 数值范围校验。
                if param.min_value is not None and value < param.min_value:
                    raise ValueError(
                        f"Parameter '{param.name}' must be >= {param.min_value}"
                    )
                if param.max_value is not None and value > param.max_value:
                    raise ValueError(
                        f"Parameter '{param.name}' must be <= {param.max_value}"
                    )

                # 正则表达式校验，适用于字符串格式要求。
                if param.pattern:
                    import re

                    if not re.match(param.pattern, str(value)):
                        raise ValueError(
                            f"Parameter '{param.name}' does not match pattern {param.pattern}"
                        )

        # 检查是否存在未声明的多余参数。
        known_params = {p.name for p in self.parameters}
        unknown = set(kwargs.keys()) - known_params
        if unknown:
            raise ValueError(f"Unknown parameters: {unknown}")

    def _validate_type(self, value: Any, expected_type: ToolParameterType) -> bool:
        """Check whether a value has the expected Python type for a tool parameter."""
        type_map = {
            ToolParameterType.STRING: str,
            ToolParameterType.INTEGER: int,
            ToolParameterType.FLOAT: (int, float),
            ToolParameterType.BOOLEAN: bool,
            ToolParameterType.ARRAY: list,
            ToolParameterType.OBJECT: dict,
            ToolParameterType.FILE: str,  # File paths are strings
        }

        expected = type_map.get(expected_type)
        if expected:
            # isinstance 可以同时检查 tuple 中的多种类型。
            return isinstance(value, expected)
        # 如果遇到未知类型，则返回 False，让调用方报告错误。
        return False

    def _get_tracker_key(
        self, session_id: Optional[str], agent_id: Optional[str] = None
    ) -> str:
        """Generate a stable key for rate limiting.

        优先使用 session_id，如果没有则使用 agent_id；如果两者都没有则为当前请求生成唯一 request_id。
        """
        if session_id:
            # 同一个 session 下请求共享限流记录。
            return f"session:{session_id}"
        if agent_id:
            # 没有 session 时，使用 agent_id 作为后备。
            return f"agent:{agent_id}"

        # 无 session/agent 时使用请求级别 UUID，避免并发请求互相影响。
        if self._current_request_id is None:
            self._current_request_id = uuid.uuid4().hex[:8]
        return f"request:{self._current_request_id}"

    def _reset_request_id(self) -> None:
        """Reset the request-scoped tracker ID.

        这一步确保在没有 session 或 agent 上下文时，每次 execute() 都能产生独立的 request 追踪键。
        """
        self._current_request_id = None

    def _get_retry_after(self, tracker_key: str) -> Optional[float]:
        """Calculate how many seconds to wait before allowing another execution."""
        if not self.metadata.rate_limit:
            # 没有设置限流则直接允许执行。
            return None

        if self.metadata.rate_limit >= RATE_LIMIT_HIGH_THROUGHPUT_THRESHOLD:
            # 高吞吐工具不使用限流检查。
            return None

        if tracker_key not in self._execution_tracker:
            # 该 key 之前没有执行记录，可以直接执行。
            return None

        last_execution = self._execution_tracker[tracker_key]
        min_interval = timedelta(seconds=60.0 / self.metadata.rate_limit)
        elapsed = datetime.now() - last_execution

        if elapsed >= min_interval:
            # 已经过了限流间隔，同一个 session/agent 可以继续执行。
            return None

        # 计算还需要等待的秒数。
        remaining = min_interval - elapsed
        return remaining.total_seconds()

    def get_schema(self) -> Dict[str, Any]:
        """Build JSON schema compatible with OpenAI function calling."""
        properties = {}
        required = []

        for param in self.parameters:
            # 每个参数映射为 JSON schema 属性。
            prop = {
                "type": param.type.value,
                "description": param.description,
            }

            # 数组类型需要指定 items 字段。
            if param.type == ToolParameterType.ARRAY:
                prop["items"] = {"type": "string"}

            # 可选的校验规则和默认值也会被写入 schema。
            if param.enum:
                prop["enum"] = param.enum
            if param.min_value is not None:
                prop["minimum"] = param.min_value
            if param.max_value is not None:
                prop["maximum"] = param.max_value
            if param.pattern:
                prop["pattern"] = param.pattern
            if param.default is not None:
                prop["default"] = param.default
            if param.items:
                prop["items"] = param.items

            properties[param.name] = prop

            # 必填参数写入 required 列表。
            if param.required:
                required.append(param.name)

        return {
            "name": self.metadata.name,
            "description": self.metadata.description,
            "parameters": {
                "type": "object",
                "properties": properties,
                "required": required,
            },
        }

    def __repr__(self) -> str:
        return f"<Tool: {self.metadata.name} v{self.metadata.version}>'"
