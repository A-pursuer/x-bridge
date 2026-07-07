package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// openTemp 在临时目录建一个全新的 SQLite 库并返回 Store。
func openTemp(t *testing.T) Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bridge.db")
	st, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite：%v", err)
	}
	t.Cleanup(func() { _ = st.(*sqliteStore).db.Close() })
	return st
}

func TestXuiPanelCRUD(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)

	p := XuiPanelRow{Name: "tokyo", APIHost: "http://100.64.0.1:2053", APIToken: "tok", TimeoutSec: 15}
	if err := st.CreateXuiPanel(ctx, p); err != nil {
		t.Fatalf("CreateXuiPanel：%v", err)
	}
	// 重名 → ErrAlreadyExists。
	if err := st.CreateXuiPanel(ctx, p); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("重名应返回 ErrAlreadyExists，实际：%v", err)
	}
	// Get。
	got, err := st.GetXuiPanel(ctx, "tokyo")
	if err != nil {
		t.Fatalf("GetXuiPanel：%v", err)
	}
	if got.APIHost != p.APIHost {
		t.Fatalf("APIHost 不一致：%q", got.APIHost)
	}
	// 不存在 → ErrNotFound。
	if _, err := st.GetXuiPanel(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在应返回 ErrNotFound，实际：%v", err)
	}
	// Update。
	got.APIToken = "newtok"
	if err := st.UpdateXuiPanel(ctx, got); err != nil {
		t.Fatalf("UpdateXuiPanel：%v", err)
	}
	after, _ := st.GetXuiPanel(ctx, "tokyo")
	if after.APIToken != "newtok" {
		t.Fatalf("Update 未生效：%q", after.APIToken)
	}
	// List。
	list, err := st.ListXuiPanels(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListXuiPanels 期望 1 条，实际 %d，err=%v", len(list), err)
	}
	// Delete + 幂等。
	if err := st.DeleteXuiPanel(ctx, "tokyo"); err != nil {
		t.Fatalf("DeleteXuiPanel：%v", err)
	}
	if err := st.DeleteXuiPanel(ctx, "tokyo"); err != nil {
		t.Fatalf("DeleteXuiPanel 应幂等：%v", err)
	}
}

func TestCountBridgesByPanel(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)

	if err := st.CreateXuiPanel(ctx, XuiPanelRow{Name: "p1", APIHost: "http://x:1", APIToken: "t"}); err != nil {
		t.Fatalf("CreateXuiPanel：%v", err)
	}
	if err := st.CreateBridge(ctx, BridgeRow{
		Name: "b1", XboardNodeID: 1, XuiPanel: "p1", XuiInboundID: 1, Protocol: "vless", Enable: true,
	}); err != nil {
		t.Fatalf("CreateBridge：%v", err)
	}
	n, err := st.CountBridgesByPanel(ctx, "p1")
	if err != nil || n != 1 {
		t.Fatalf("CountBridgesByPanel 期望 1，实际 %d，err=%v", n, err)
	}
	if n, _ := st.CountBridgesByPanel(ctx, "p-none"); n != 0 {
		t.Fatalf("无引用面板应返回 0，实际 %d", n)
	}
}

// TestBridgeRoundTripPanelField 确认 xui_panel 列在写入/读取往返中不丢。
func TestBridgeRoundTripPanelField(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	_ = st.CreateXuiPanel(ctx, XuiPanelRow{Name: "osaka", APIHost: "http://x:1", APIToken: "t"})
	if err := st.CreateBridge(ctx, BridgeRow{
		Name: "b", XboardNodeID: 2, XuiPanel: "osaka", XuiInboundID: 3, Protocol: "hysteria2", Enable: true,
	}); err != nil {
		t.Fatalf("CreateBridge：%v", err)
	}
	got, err := st.GetBridge(ctx, "b")
	if err != nil {
		t.Fatalf("GetBridge：%v", err)
	}
	if got.XuiPanel != "osaka" {
		t.Fatalf("xui_panel 往返丢失：%q", got.XuiPanel)
	}
}

// TestLegacyXuiMigration 验证旧"单例 xui.* settings"库升级后自动生成
// default 面板、回填桥接引用、清理残留键，且幂等。
func TestLegacyXuiMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")

	// 第一步：正常打开建 schema，然后把库改造成"旧单例"形态。
	st, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("首次 OpenSQLite：%v", err)
	}
	db := st.(*sqliteStore).db

	// 造一条 xui_panel 为空的桥接（模拟旧库：迁移前没有 xui_panel 概念）。
	if err := st.CreateBridge(ctx, BridgeRow{
		Name: "legacy-b", XboardNodeID: 1, XuiPanel: "", XuiInboundID: 1, Protocol: "vless", Enable: true,
	}); err != nil {
		t.Fatalf("CreateBridge：%v", err)
	}
	// 清空迁移可能已建的面板（保证前置：xui_panels 空），并写入旧 xui.* settings。
	if _, err := db.ExecContext(ctx, `DELETE FROM xui_panels`); err != nil {
		t.Fatalf("清空 xui_panels：%v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE bridges SET xui_panel=''`); err != nil {
		t.Fatalf("重置 xui_panel：%v", err)
	}
	legacy := map[string]string{
		"xui.api_host":        "http://127.0.0.1:2053",
		"xui.base_path":       "/legacy",
		"xui.api_token":       "LEGACYTOKEN0000",
		"xui.timeout_sec":     "20",
		"xui.skip_tls_verify": "true",
		"xui.username":        "old", // v0.4/v0.5 残留，应一并清掉
	}
	for k, v := range legacy {
		if _, err := db.ExecContext(ctx,
			`INSERT OR REPLACE INTO settings(key,value,updated_at) VALUES(?,?,0)`, k, v); err != nil {
			t.Fatalf("写入旧 setting %s：%v", k, err)
		}
	}
	_ = db.Close()

	// 第二步：重新打开——runLegacyXuiMigration 应在 OpenSQLite 内触发。
	st2, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("二次 OpenSQLite（触发迁移）：%v", err)
	}
	db2 := st2.(*sqliteStore).db
	t.Cleanup(func() { _ = db2.Close() })

	// 断言：default 面板已建，字段来自旧 settings。
	panel, err := st2.GetXuiPanel(ctx, legacyDefaultPanelName)
	if err != nil {
		t.Fatalf("迁移后应存在 default 面板：%v", err)
	}
	if panel.APIHost != "http://127.0.0.1:2053" || panel.BasePath != "/legacy" ||
		panel.APIToken != "LEGACYTOKEN0000" || panel.TimeoutSec != 20 || !panel.SkipTLSVerify {
		t.Fatalf("default 面板字段迁移不完整：%+v", panel)
	}
	// 断言：桥接 xui_panel 已回填为 default。
	b, err := st2.GetBridge(ctx, "legacy-b")
	if err != nil {
		t.Fatalf("GetBridge：%v", err)
	}
	if b.XuiPanel != legacyDefaultPanelName {
		t.Fatalf("桥接 xui_panel 应回填为 default，实际 %q", b.XuiPanel)
	}
	// 断言：xui.* settings 已清空。
	settings, _ := st2.ListSettings(ctx)
	for k := range settings {
		if len(k) >= 4 && k[:4] == "xui." {
			t.Fatalf("旧 xui.* 键应被清理，仍存在：%s", k)
		}
	}

	// 幂等：xui_panels 非空后再打开一次，不应重复建或报错。
	_ = db2.Close()
	st3, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("三次 OpenSQLite（幂等）：%v", err)
	}
	db3 := st3.(*sqliteStore).db
	t.Cleanup(func() { _ = db3.Close() })
	list, _ := st3.ListXuiPanels(ctx)
	if len(list) != 1 {
		t.Fatalf("幂等：应仍只有 1 个面板，实际 %d", len(list))
	}
}
