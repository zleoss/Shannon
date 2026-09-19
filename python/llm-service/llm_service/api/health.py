"""=============================================================================
文件: python/llm-service/llm_service/api/health.py
-------------------------------------------------------------------------------
【一句话功能】
  健康检查端点 /。
【关键内容】
  router :4；health_check :7 返回服务/时间戳
【协作关系】
  被 docker compose / orchestrator / 负载均衡探活调用。
=============================================================================
"""

from fastapi import APIRouter, Request
from datetime import datetime

router = APIRouter()


@router.get("/")
async def health_check(request: Request):
    """Basic health check endpoint"""
    return {
        "status": "healthy",
        "service": "shannon-llm-service",
        "timestamp": datetime.utcnow().isoformat(),
    }


@router.get("/ready")
async def readiness_check(request: Request):
    """Readiness check - verifies all dependencies are accessible"""
    providers = request.app.state.providers

    checks = {
        "providers": len(providers.providers) > 0,
        "models": len(providers.model_registry) > 0,
    }

    # In debug/dev mode, allow readiness without providers/models
    debug_mode = getattr(request.app.state.settings, "debug", False)
    all_ready = all(checks.values()) or debug_mode

    return {
        "ready": all_ready,
        "checks": checks,
        "debug_mode": debug_mode,
        "timestamp": datetime.utcnow().isoformat(),
    }


@router.get("/live")
async def liveness_check():
    """Liveness check - simple ping"""
    return {"alive": True}
