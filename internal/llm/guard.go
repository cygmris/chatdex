package llm

import (
	"fmt"
	"net"
	"net/url"
)

// requireLoopback 校验端点必须是回环地址。
//
// 这不是可配置的严格程度：会话内容含工具结果里 cat/env/curl 的明文密钥，
// 把它发到本机之外就等于泄露一份集中的凭证副本。需求 10.5 写死为不可放宽，
// 所以这里不提供任何 --allow-remote 之类的开关——远端地址一律构造失败。
func requireLoopback(endpoint string) error { return ValidateEndpoint(endpoint) }

// ValidateEndpoint 供配置保存路径复用——界面上能填不等于能存。
func ValidateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("LLM 端点不是合法 URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("LLM 端点协议只支持 http/https，得到 %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("LLM 端点缺少主机名: %q", endpoint)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// 主机名可能解析到任何地方，不做 DNS 赌博
		return fmt.Errorf("LLM 端点只允许回环地址（127.0.0.1 / ::1 / localhost），得到 %q", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("LLM 端点只允许回环地址，得到 %q —— "+
			"会话内容含明文凭证，不得发往本机之外", host)
	}
	return nil
}
