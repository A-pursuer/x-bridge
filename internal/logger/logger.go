// Package logger 把 config.Log 转成 *slog.Logger。
//
// 选择 slog（标准库）而非 zap / logrus 的原因：
//
//  1. 零外部依赖：保持二进制小巧、SBOM 干净；
//  2. 标准化 KV 字段：所有 kv 都被 slog 序列化为机器可读 JSON，
//     便于运维收到日志后用 jq / Loki 直接抓字段；
//  3. 性能足够：本中间件单实例日志吞吐 < 1k QPS，远低于 slog 的瓶颈。
//
// 文件滚动策略：
//
//	不引入 lumberjack 等第三方滚动库（避免再增加依赖）。当用户配置
//	了 max_size_mb 时，本包内部用一个简单的"启动期检测 + 改名归档"
//	实现按大小滚动；MaxBackups / MaxAgeDays 的清理在打开时一次性执行。
//	这种简化版策略对中间件的"低写入频率"场景已足够，不追求实时滚动。
//
// JSON 字段约定（v0.8.4 专业化）：
//
//	ts      RFC3339Nano UTC 时间戳（rename 自 slog 默认 "time"，与 Loki /
//	        Elastic Common Schema 等聚合系统默认字段名对齐）
//	level   小写："debug" / "info" / "warn" / "error"（slog 默认是大写）
//	msg     人类可读消息文本
//	service "xboard-xui-bridge"（识别本中间件，对多服务聚合系统友好）
//	version 编译期注入的版本号；多版本灰度时按版本切片分析
//	host    主机名（os.Hostname()）；多节点部署时区分日志来源
//	pid     进程 PID；便于在 systemd restart 后区分前后实例
//	module  组件标识（如 web / xui / supervisor / sync）；由各组件
//	        在自己的 With("module", "...") 中追加
//	bridge / loop / trace_id  详见 internal/sync engine 注释
package logger

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xboard-bridge/xboard-xui-bridge/internal/config"
)

// serviceName 是注入到所有日志的 service 字段值；与二进制 / systemd unit /
// 文档中的项目名严格对齐。集中常量便于未来 rename 时一处修改。
const serviceName = "xboard-xui-bridge"

// JSON 字段键。集中常量便于 grep / 日志聚合工具的字段映射配置。
const (
	keyTimestamp = "ts"
	keyLevel     = "level"
	keyService   = "service"
	keyVersion   = "version"
	keyHost      = "host"
	keyPID       = "pid"
)

// BaseAttrs 是所有日志共享的"基线 attrs"。
//
// version 由 main.go 在调用 New 时传入（编译期 -ldflags 注入到
// main.version）；hostname / pid 由 New 自动捕获。多节点 / 多版本部署的
// 运维通过这三项标签精确切片日志。
type BaseAttrs struct {
	Version string
}

// New 根据配置构造 *slog.Logger，并返回一个 closer 回调用于在 main 退出时
// 释放可能持有的文件句柄；调用方应当 defer closer()。
//
// 行为契约：
//
//	cfg.File == ""              → 写到 stdout，closer 是 no-op
//	cfg.File != ""              → 以 append 模式打开文件；不存在则创建
//	cfg.MaxSizeMB > 0           → 启动时若现有文件已超阈值，先归档再打开新文件
//	cfg.MaxBackups / MaxAgeDays → 启动时清理过期归档（不在运行时持续清理）
//
// 不在运行时持续清理是有意为之：定时清理需要额外 goroutine 和锁，
// 而中间件单进程长期运行时日志写入量稳定可控，启动期一次性清理已足够。
//
// base 提供编译期 / 启动期注入的标签（version 等）；nil 时退化为空 BaseAttrs。
// 调用方约定：main.go 把 version 变量传入；测试场景可传 nil。
func New(cfg config.Log, base *BaseAttrs) (*slog.Logger, func() error, error) {
	level, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, nil, err
	}

	var w io.Writer
	closer := func() error { return nil }

	if strings.TrimSpace(cfg.File) == "" {
		w = os.Stdout
	} else {
		f, c, err := openLogFile(cfg)
		if err != nil {
			return nil, nil, err
		}
		w = f
		closer = c
	}

	// JSON 是首选格式：易被日志聚合系统识别字段。
	// 时间字段 RFC3339Nano 精度，便于排查毫秒级时序问题。
	//
	// 字段重命名（v0.8.4 起）：
	//   slog.TimeKey  → ts     与 Loki / ECS 默认字段对齐
	//   slog.LevelKey → 值改小写  与多数日志聚合系统的语义对齐
	//   slog.MsgKey   → 保留 msg
	//
	// 强制 UTC：开发机（如 Asia/Shanghai）与生产容器（通常 UTC）日志时区
	// 不一致会让运维跨环境对照 sync 周期 / 上报 LAST_PUSH_AT 时频繁踩
	// "差 8 小时"坑。一律 UTC 与 store 表中 unix 时间戳的解析基线对齐。
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// 仅 top-level（groups 为空）的字段名做重命名——避免嵌套
			// 用户字段被误改。
			if len(groups) != 0 {
				return a
			}
			switch a.Key {
			case slog.TimeKey:
				if t, ok := a.Value.Any().(time.Time); ok {
					return slog.Attr{
						Key:   keyTimestamp,
						Value: slog.StringValue(t.UTC().Format(time.RFC3339Nano)),
					}
				}
			case slog.LevelKey:
				if lv, ok := a.Value.Any().(slog.Level); ok {
					return slog.String(keyLevel, strings.ToLower(lv.String()))
				}
			}
			return a
		},
	})

	logger := slog.New(handler).With(baseAttrs(base)...)
	return logger, closer, nil
}

// baseAttrs 把 BaseAttrs + 运行时自动捕获的 host / pid 拼成 slog 的
// With 参数切片。所有字段都是字符串 / int，便于聚合系统索引。
func baseAttrs(base *BaseAttrs) []any {
	version := "dev"
	if base != nil && strings.TrimSpace(base.Version) != "" {
		version = base.Version
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return []any{
		keyService, serviceName,
		keyVersion, version,
		keyHost, host,
		keyPID, os.Getpid(),
	}
}

// parseLevel 把字符串等级转为 slog.Level。
// 与 config.Validate 对齐的合法值：debug / info / warn / error。
func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error", "err":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("无法识别的日志等级：%q", s)
	}
}

// openLogFile 处理"按大小滚动 + 启动清理"两步：
//  1. 若现有日志已超 MaxSizeMB，先归档为 file.<ts>。
//  2. 应用 MaxBackups / MaxAgeDays 清理历史归档。
//  3. 以 O_APPEND|O_CREATE 打开主文件供后续追加写入。
func openLogFile(cfg config.Log) (*os.File, func() error, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.File), 0o755); err != nil {
		return nil, nil, fmt.Errorf("创建日志目录失败：%w", err)
	}

	// 步骤 1：检查现有文件大小，必要时先归档。
	if cfg.MaxSizeMB > 0 {
		if info, err := os.Stat(cfg.File); err == nil {
			thresholdBytes := int64(cfg.MaxSizeMB) * 1024 * 1024
			if info.Size() >= thresholdBytes {
				archive := fmt.Sprintf("%s.%s", cfg.File, time.Now().Format("20060102-150405"))
				if err := os.Rename(cfg.File, archive); err != nil {
					return nil, nil, fmt.Errorf("归档旧日志失败：%w", err)
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, fmt.Errorf("检查日志文件状态失败：%w", err)
		}
	}

	// 步骤 2：清理超出限制的归档（按文件名时间戳和文件 mtime 双重判断）。
	cleanupArchives(cfg)

	// 步骤 3：打开主文件。0o644 是日志文件惯例：所属者读写，其他人只读。
	f, err := os.OpenFile(cfg.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("打开日志文件 %q：%w", cfg.File, err)
	}
	closer := func() error { return f.Close() }
	return f, closer, nil
}

// cleanupArchives 在启动期一次性清理过期归档。
//
// 归档命名约定：<file>.<YYYYmmdd-HHMMSS>。仅识别这种命名的归档，避免误删
// 用户手工放置在同目录的其他文件。
//
// 失败一律仅打印 warning（输出到 stderr），不阻断主流程——日志清理失败不
// 应该导致整个中间件无法启动。
func cleanupArchives(cfg config.Log) {
	dir := filepath.Dir(cfg.File)
	base := filepath.Base(cfg.File)
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[warn] 读取日志目录 %q 失败：%v\n", dir, err)
		return
	}

	type archive struct {
		path    string
		modTime time.Time
	}
	var archives []archive

	prefix := base + "."
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		// 仅识别符合时间戳后缀的命名，避免误伤用户文件。
		suffix := strings.TrimPrefix(name, prefix)
		if _, perr := time.Parse("20060102-150405", suffix); perr != nil {
			continue
		}
		full := filepath.Join(dir, name)
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		archives = append(archives, archive{path: full, modTime: info.ModTime()})
	}

	// 按 mtime 倒序：保留最新的 MaxBackups 份。
	sort.Slice(archives, func(i, j int) bool {
		return archives[i].modTime.After(archives[j].modTime)
	})

	now := time.Now()
	for i, a := range archives {
		drop := false
		if cfg.MaxBackups > 0 && i >= cfg.MaxBackups {
			drop = true
		}
		if cfg.MaxAgeDays > 0 && now.Sub(a.modTime) > time.Duration(cfg.MaxAgeDays)*24*time.Hour {
			drop = true
		}
		if drop {
			if rerr := os.Remove(a.path); rerr != nil {
				fmt.Fprintf(os.Stderr, "[warn] 清理归档日志 %q 失败：%v\n", a.path, rerr)
			}
		}
	}
}
