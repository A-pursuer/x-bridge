package config

import (
	"strings"
	"testing"
)

// baseValidRoot 造一个"最小合法"的 Root：一个面板 + 一条引用它的桥接。
// State.Database 必填（Validate 的"鸡生蛋"前置）。
func baseValidRoot() *Root {
	return &Root{
		State: State{Database: "/tmp/x.db"},
		XuiPanels: []XuiPanel{
			{Name: "default", Xui: Xui{APIHost: "http://127.0.0.1:2053", APIToken: "AAAABBBBCCCCDDDD"}},
		},
		Bridges: []Bridge{
			{Name: "b1", XboardNodeID: 1, XuiPanel: "default", XuiInboundID: 1, Protocol: "vless", Enable: true},
		},
	}
}

func TestValidateHappyPath(t *testing.T) {
	r := baseValidRoot()
	if err := r.Validate(); err != nil {
		t.Fatalf("合法配置不应报错：%v", err)
	}
	// node_type 应按 protocol 回填。
	if r.Bridges[0].XboardNodeType != "vless" {
		t.Fatalf("XboardNodeType 应回填为 vless，实际 %q", r.Bridges[0].XboardNodeType)
	}
}

func TestValidateDanglingPanelRef(t *testing.T) {
	r := baseValidRoot()
	r.Bridges[0].XuiPanel = "ghost"
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "不存在的面板") {
		t.Fatalf("悬空面板引用应报错，实际：%v", err)
	}
}

func TestValidateEmptyPanelRef(t *testing.T) {
	r := baseValidRoot()
	r.Bridges[0].XuiPanel = ""
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "xui_panel 不可为空") {
		t.Fatalf("空面板引用应报错，实际：%v", err)
	}
}

func TestValidateDuplicatePanelName(t *testing.T) {
	r := baseValidRoot()
	r.XuiPanels = append(r.XuiPanels, XuiPanel{
		Name: "default", Xui: Xui{APIHost: "http://127.0.0.1:2054", APIToken: "EEEEFFFFGGGGHHHH"},
	})
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("面板重名应报错，实际：%v", err)
	}
}

func TestValidateHysteria2NodeTypeMapping(t *testing.T) {
	r := baseValidRoot()
	r.Bridges[0].Protocol = "hysteria2"
	r.Bridges[0].XboardNodeType = "" // 留空触发回填
	if err := r.Validate(); err != nil {
		t.Fatalf("hysteria2 桥接不应报错：%v", err)
	}
	// hysteria2 在 Xboard 端 node_type 应映射为 hysteria。
	if r.Bridges[0].XboardNodeType != "hysteria" {
		t.Fatalf("hysteria2 的 node_type 应回填为 hysteria，实际 %q", r.Bridges[0].XboardNodeType)
	}
}

func TestValidateInvalidProtocol(t *testing.T) {
	r := baseValidRoot()
	r.Bridges[0].Protocol = "qq-protocol"
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "protocol 取值非法") {
		t.Fatalf("非法协议应报错，实际：%v", err)
	}
}

func TestNormalizeXuiProbe(t *testing.T) {
	// 合法：带尾斜杠的 host 应被去掉；base_path 归一化。
	out, err := NormalizeXuiProbe(Xui{
		APIHost:  "http://127.0.0.1:2053/",
		BasePath: "xui/",
		APIToken: "AAAABBBBCCCCDDDD",
	})
	if err != nil {
		t.Fatalf("合法探测参数不应报错：%v", err)
	}
	if out.APIHost != "http://127.0.0.1:2053" {
		t.Fatalf("host 尾斜杠应被去除，实际 %q", out.APIHost)
	}
	if out.BasePath != "/xui" {
		t.Fatalf("base_path 应归一化为 /xui，实际 %q", out.BasePath)
	}
	if out.TimeoutSec != defaultXuiTimeoutSec {
		t.Fatalf("timeout<=0 应补默认，实际 %d", out.TimeoutSec)
	}

	// 缺 host。
	if _, err := NormalizeXuiProbe(Xui{APIToken: "AAAABBBBCCCCDDDD"}); err == nil {
		t.Fatalf("缺 api_host 应报错")
	}
	// 缺 token。
	if _, err := NormalizeXuiProbe(Xui{APIHost: "http://127.0.0.1:2053"}); err == nil {
		t.Fatalf("缺 api_token 应报错")
	}
	// host 缺 scheme。
	if _, err := NormalizeXuiProbe(Xui{APIHost: "127.0.0.1:2053", APIToken: "AAAABBBBCCCCDDDD"}); err == nil {
		t.Fatalf("host 缺 scheme 应报错")
	}
}
