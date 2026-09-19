"""=============================================================================
文件: python/llm-service/llm_service/roles/swarm/__init__.py
-------------------------------------------------------------------------------
【一句话功能】 Swarm V2 提示词定义入口，集中管理所有 swarm 相关 prompt
【关键内容】 agent_protocol.py：agent 核心协议（动作/阶段/规则）
             lead_protocol.py：lead 编排与评估 prompt
             role_prompts.py：角色专属 prompt 片段
【协作关系】 被 swarm API 端点加载；与 orchestrator 的 SwarmWorkflow 配合使用
============================================================================="""

from .agent_protocol import AGENT_LOOP_SYSTEM_PROMPT, COMMON_PROTOCOL_BASE, get_work_protocol
from .lead_protocol import LEAD_SYSTEM_PROMPT
from .role_prompts import SWARM_ROLE_PROMPTS, get_swarm_role_catalog

__all__ = [
    "AGENT_LOOP_SYSTEM_PROMPT",
    "COMMON_PROTOCOL_BASE",
    "get_work_protocol",
    "LEAD_SYSTEM_PROMPT",
    "SWARM_ROLE_PROMPTS",
    "get_swarm_role_catalog",
]
