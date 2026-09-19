"""=============================================================================
文件: python/llm-service/llm_service/tools/__init__.py
-------------------------------------------------------------------------------
【一句话功能】 Shannon 工具系统入口，导出核心工具抽象与注册表
【关键内容】 导出 Tool / ToolResult / ToolParameter / ToolMetadata 基类
             导出 ToolRegistry / get_registry 注册表函数
【协作关系】 被所有工具模块 import；被 tool_executor 用于工具发现与路由
============================================================================="""

from .base import Tool, ToolResult, ToolParameter, ToolMetadata
from .registry import ToolRegistry, get_registry

__all__ = [
    "Tool",
    "ToolResult",
    "ToolParameter",
    "ToolMetadata",
    "ToolRegistry",
    "get_registry",
]
