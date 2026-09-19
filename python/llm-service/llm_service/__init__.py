"""=============================================================================
文件: python/llm-service/llm_service/__init__.py
-------------------------------------------------------------------------------
【一句话功能】 LLM Service 包的 __init__。把 grpc_gen 加入 sys.path 以支持
protobuf 导入（由 make proto 生成）。
【定位】 包初始化，无业务逻辑。
=============================================================================

Shannon LLM Service — Provider-agnostic LLM integration"""

import sys
import logging
from pathlib import Path

logger = logging.getLogger(__name__)

# Add grpc_gen to sys.path for protobuf imports
_grpc_gen = Path(__file__).parent / "grpc_gen"
if _grpc_gen.exists():
    sys.path.insert(0, str(_grpc_gen))
    logger.debug(f"Added grpc_gen to sys.path: {_grpc_gen}")
else:
    logger.warning(
        f"grpc_gen directory not found at {_grpc_gen}, protobuf imports may fail"
    )

__version__ = "0.1.0"
