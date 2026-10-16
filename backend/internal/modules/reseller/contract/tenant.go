package contract

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/dujiao-next/internal/config"
)

type tenantContextKey struct{}

// TenantContext 表示主站或分销站请求上下文。
type TenantContext struct {
	Host              string
	IsMain            bool
	ResellerID        *uint
	ResellerUserID    uint
	PrimaryDomain     string
	Unavailable       bool
	UnavailableReason string
}

// IsReseller 判断当前上下文是否为可用的分销站请求。
func (tenant TenantContext) IsReseller() bool {
	return tenant.ResellerID != nil && !tenant.IsMain && !tenant.Unavailable
}

// MainTenantContext 构造主站租户上下文。
func MainTenantContext(host string) TenantContext {
	return TenantContext{Host: NormalizeHost(host), IsMain: true}
}

// ResellerTenantContext 构造可用的分销站租户上下文。
func ResellerTenantContext(host string, resellerID uint, resellerUserID uint, primaryDomain string) TenantContext {
	id := resellerID
	return TenantContext{
		Host:           NormalizeHost(host),
		IsMain:         false,
		ResellerID:     &id,
		ResellerUserID: resellerUserID,
		PrimaryDomain:  NormalizeHost(primaryDomain),
	}
}

// UnavailableTenantContext 构造不可用租户上下文（未知域名或未激活等）。
func UnavailableTenantContext(host string, reason string) TenantContext {
	return TenantContext{
		Host:              NormalizeHost(host),
		Unavailable:       true,
		UnavailableReason: strings.TrimSpace(reason),
	}
}

// WithTenantContext 将租户上下文写入 context。
func WithTenantContext(ctx context.Context, tenant TenantContext) context.Context {
	return context.WithValue(ctx, tenantContextKey{}, tenant)
}

// TenantFromContext 从 context 读取租户上下文。
func TenantFromContext(ctx context.Context) (TenantContext, bool) {
	if ctx == nil {
		return TenantContext{}, false
	}
	tenant, ok := ctx.Value(tenantContextKey{}).(TenantContext)
	return tenant, ok
}

// NormalizeHost 归一化请求 Host（小写、去端口、去尾点）。
func NormalizeHost(raw string) string {
	host := strings.TrimSpace(strings.ToLower(raw))
	if host == "" {
		return ""
	}
	if strings.HasPrefix(host, "[") {
		if parsed, _, err := net.SplitHostPort(host); err == nil {
			host = strings.Trim(parsed, "[]")
		}
	} else if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(host, ".")
	host = strings.Trim(host, "[]")
	return host
}

// RequestHost rejects ambiguous HTTP authorities before tenant lookup.
func RequestHost(raw string) string {
	if raw == "" || strings.ContainsAny(raw, " \t\r\n,/@\\?#%") {
		return ""
	}
	host := NormalizeHost(raw)
	if host == "" {
		return ""
	}
	if strings.Contains(raw, ":") {
		h, p, e := net.SplitHostPort(raw)
		if e != nil {
			return ""
		}
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 || h == "" {
			return ""
		}
	}
	if net.ParseIP(host) != nil {
		return host
	}
	if len(host) > 253 {
		return ""
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return ""
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				return ""
			}
		}
	}
	return host
}

// Forwarded Host is opt-in AND tied to the direct TCP peer, never to XFF.
func ResolveRequestHost(req *http.Request, cfg config.ResellerConfig) string {
	if req == nil {
		return ""
	}
	raw := req.Host
	if cfg.TrustedForwardedHost && len(req.Header.Values("X-Forwarded-Host")) > 0 {
		values := req.Header.Values("X-Forwarded-Host")
		if len(values) != 1 {
			return ""
		}
		peer, _, err := net.SplitHostPort(req.RemoteAddr)
		if err != nil {
			return ""
		}
		ip := net.ParseIP(peer)
		trusted := false
		for _, v := range cfg.TrustedForwardedHostProxies {
			if net.ParseIP(v) != nil && ip.Equal(net.ParseIP(v)) {
				trusted = true
			}
			if _, network, e := net.ParseCIDR(v); e == nil && network.Contains(ip) {
				trusted = true
			}
		}
		if !trusted {
			return ""
		}
		raw = values[0]
	}
	return RequestHost(raw)
}
