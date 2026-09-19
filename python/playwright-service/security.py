"""=============================================================================
文件: python/playwright-service/security.py
-------------------------------------------------------------------------------
【一句话功能】
  playwright-service SSRF 防护辅助（stdlib 实现，便于单测）。
【关键内容】
  阻断 RFC1918 / loopback / CGNAT 等私有/元数据目标
  基于 ipaddress + socket 解析与判定
【协作关系】
  被 app.py capture / browser action 在请求外部 URL 前调用。
=============================================================================
-------------------------------------------------------------------------------
【原 docstring】
  SSRF protection helpers for playwright-service.

  This module is stdlib-only so it can be unit tested without Playwright installed.
=============================================================================
"""

import ipaddress
import socket
from typing import List
from urllib.parse import urlparse


def _resolve_host_ips(hostname: str) -> List[ipaddress._BaseAddress]:
    """Resolve hostname to all IPs (IPv4 + IPv6).

    Returns an empty list if resolution fails.
    """
    try:
        return [ipaddress.ip_address(hostname)]
    except ValueError:
        pass

    try:
        infos = socket.getaddrinfo(hostname, None)
    except socket.gaierror:
        return []

    out: List[ipaddress._BaseAddress] = []
    seen = set()
    for family, _, _, _, sockaddr in infos:
        ip_str = None
        if family == socket.AF_INET:
            ip_str = sockaddr[0]
        elif family == socket.AF_INET6:
            ip_str = sockaddr[0]
        if not ip_str or ip_str in seen:
            continue
        seen.add(ip_str)
        try:
            out.append(ipaddress.ip_address(ip_str))
        except ValueError:
            continue
    return out


def validate_url_for_ssrf(url: str) -> None:
    """Validate URL for SSRF protection.

    Raises:
        ValueError: If the URL is invalid or points to non-public IP space.
    """
    parsed = urlparse(url)

    if parsed.scheme not in ("http", "https"):
        raise ValueError("Only HTTP/HTTPS URLs allowed")

    hostname = parsed.hostname
    if not hostname:
        raise ValueError("Invalid URL: no hostname")

    ips = _resolve_host_ips(hostname)
    if not ips:
        raise ValueError("Hostname does not resolve")

    # Block if hostname resolves to any non-global IP (RFC1918, loopback, link-local,
    # reserved, multicast, CGNAT, etc).
    if any(not ip.is_global for ip in ips):
        raise ValueError("Access to internal URLs is not allowed")

