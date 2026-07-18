package web

import (
	"net/http/httptest"
	"testing"
)

func TestExactBridgePathNamePreservesLegacyWhitespace(t *testing.T) {
	r := httptest.NewRequest("DELETE", "/api/bridges/demo%20", nil)
	r.SetPathValue("name", "demo ")

	got, ok := exactBridgePathName(r)
	if !ok {
		t.Fatal("带尾部空白的存量主键不应被判为空")
	}
	if got != "demo " {
		t.Fatalf("必须保留原始主键以精确删除，实际得到 %q", got)
	}
}

func TestExactBridgePathNameRejectsWhitespaceOnly(t *testing.T) {
	r := httptest.NewRequest("DELETE", "/api/bridges/%20", nil)
	r.SetPathValue("name", " \t")

	if _, ok := exactBridgePathName(r); ok {
		t.Fatal("纯空白主键应被拒绝")
	}
}
