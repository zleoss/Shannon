"""=============================================================================
文件: python/llm-service/llm_service/tools/vendor_adapters/__init__.py
-------------------------------------------------------------------------------
【一句话功能】
  厂商 API 转换适配器注册表（OpenAPI 工具用）。
【关键内容】
  get_vendor_adapter :21 按 vendor 名取适配器
  ALLOWED_VENDORS 白名单（安全控制）
【协作关系】
  被 OpenAPITool 调用以在请求/响应阶段做厂商特定改写。
=============================================================================
-------------------------------------------------------------------------------
【原 docstring】
  Vendor adapters for domain-specific API transformations.

  This module provides adapter registries for vendor-specific tool integrations.

  get_vendor_adapter() - For OpenAPI-based tools
    - Purpose: Transform request/response bodies for OpenAPI specs
    - Security: Whitelist-based (must manually add vendors to ALLOWED_VENDORS)
    - Usage: OpenAPITool uses these adapters to modify API calls

  ADDING NEW ADAPTERS:

  For OpenAPI tools:
    1. Create vendor_adapters/myvendor/adapter.py with MyVendorAdapter class
    2. Add "myvendor" to ALLOWED_VENDORS whitelist (see get_vendor_adapter)
    3. Add explicit import in get_vendor_adapter() function

  See docs/vendor-adapters.md for complete guide.
=============================================================================
"""


def get_vendor_adapter(name: str):
    """Return a vendor adapter for OpenAPI-based tools.

    Uses whitelist-based security - vendors must be explicitly registered.

    Args:
        name: Vendor identifier

    Returns:
        Vendor adapter instance or None if vendor not found/not whitelisted
    """
    if not name:
        return None

    if ".." in name or "/" in name or "\\" in name:
        return None

    if not name.replace("_", "").replace("-", "").isalnum():
        return None

    vendor_name = name.lower()

    ALLOWED_VENDORS = {
        # Add your vendor names here as you implement them
        # "myvendor",
    }

    if vendor_name not in ALLOWED_VENDORS:
        return None

    try:
        # Example vendor adapter registration:
        # if vendor_name == "myvendor":
        #     from .myvendor.adapter import MyVendorAdapter
        #     return MyVendorAdapter()
        pass

    except ImportError:
        return None
    except Exception:
        return None

    return None
