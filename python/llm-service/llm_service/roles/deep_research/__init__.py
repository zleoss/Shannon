"""=============================================================================
文件: python/llm-service/llm_service/roles/deep_research/__init__.py
-------------------------------------------------------------------------------
【一句话功能】 Deep Research 工作流的角色预设定义
【关键内容】 deep_research_agent：主任务 agent；research_refiner：查询扩展与规划
             domain_discovery：公司域名识别；domain_prefetch：网站内容预取
【协作关系】 被 ResearchWorkflow 加载使用；各预设与 orchestrator 的 role 系统对接
============================================================================="""

from .deep_research_agent import DEEP_RESEARCH_AGENT_PRESET
from .quick_research_agent import QUICK_RESEARCH_AGENT_PRESET
from .research_refiner import RESEARCH_REFINER_PRESET
from .research_supervisor import RESEARCH_SUPERVISOR_IDENTITY, DOMAIN_ANALYSIS_HINT
from .domain_discovery import DOMAIN_DISCOVERY_PRESET
from .domain_prefetch import DOMAIN_PREFETCH_PRESET

__all__ = [
    "DEEP_RESEARCH_AGENT_PRESET",
    "QUICK_RESEARCH_AGENT_PRESET",
    "RESEARCH_REFINER_PRESET",
    "RESEARCH_SUPERVISOR_IDENTITY",
    "DOMAIN_ANALYSIS_HINT",
    "DOMAIN_DISCOVERY_PRESET",
    "DOMAIN_PREFETCH_PRESET",
]
