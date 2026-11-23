#!/bin/sh
# Termius Snippet 名称：203. 部署 xboard-xui-bridge（fork 供应链固化版）
# 用途：从自有 fork（A-pursuer/x-bridge）的 Release 安装中间件，
#       并把 Web 面板绑定到 Tailscale 内网 IP，公网不可达
#
# 说明：
# - 外层仅作为 launcher，真实逻辑写入临时脚本后以独立 bash 子进程同步执行，
#   避免子进程内 set -e/exit 终止当前 Termius 交互式会话
# - 使用 flock 防止重复并发执行
# - 供应链：install.sh 与二进制均来自 A-pursuer fork（自有 CI 构建 + SHA256 校验），
#   下载后先校验脚本内 GITHUB_REPO 指向本 fork 才执行，防止拿错/被篡改
# - install.sh 以 stdin=/dev/null 运行：其"是否放行 8787/tcp"交互提示会被自动拒绝，
#   UFW 不放行 8787（面板只走 Tailscale，公网无需可达）
# - 安装后写 systemd drop-in 把监听地址改为 <TailscaleIP>:8787，并声明依赖
#   tailscaled；开机若 Tailscale IP 尚未就绪，Restart=on-failure 会自动重试绑定
# - 重复运行 = 升级：install.sh 自带旧二进制/数据库备份；监听 drop-in 幂等重写
# - 前置：Tailscale 必须已安装并 up（本 snippet 检测不到 Tailscale IPv4 会直接退出）

# 可长期保存到 Termius Snippet：
GITHUB_REPO="A-pursuer/x-bridge"
LISTEN_PORT="8787"
BRIDGE_VERSION="v0.11.1-idempotency-20261123"
BRIDGE_LOCAL_BINARY="/usr/local/lib/xbridge-selfhost/accepted/xboard-xui-bridge"
BRIDGE_LOCAL_SHA256="cb5f2350e54266bdccd8850ba4efc4860c7729001eb2a55075f20eed58f98792"
INSTALLER_LOCAL="/usr/local/lib/xbridge-selfhost/install.sh"
INSTALLER_SHA256="426f897fa7b8ea57903e76832c99c09b5bc2f237ee53d9db45acdc233cfc824e"


_tmp_script=""
_rc=1

if ! command -v bash >/dev/null 2>&1; then
  echo "[ERROR] 未检测到 bash，请先安装 bash。" >&2

elif [ "$(id -u 2>/dev/null || echo 1)" != "0" ] && ! command -v sudo >/dev/null 2>&1; then
  echo "[ERROR] 当前用户不是 root，且未检测到 sudo；请切换 root 后运行。" >&2

elif _tmp_script="$(mktemp /tmp/termius-xui-bridge.XXXXXX.sh)"; then
  cat > "$_tmp_script" <<'__TERMIUS_BASH__'
#!/usr/bin/env bash
set -euo pipefail
umask 077

export PATH="/usr/sbin:/usr/bin:/sbin:/bin:${PATH:-}"

GITHUB_REPO="${GITHUB_REPO:-A-pursuer/x-bridge}"
LISTEN_PORT="${LISTEN_PORT:-8787}"

SERVICE_NAME="xboard-xui-bridge"
BIN_PATH="/usr/local/bin/xboard-xui-bridge"
DATA_DIR="/usr/local/xboard-xui-bridge/data"
DROPIN_DIR="/etc/systemd/system/${SERVICE_NAME}.service.d"
DROPIN_FILE="${DROPIN_DIR}/listen.conf"
LOCK_FILE="/var/lock/termius-xui-bridge.lock"
INSTALL_SH_URL="${INSTALL_SH_URL:-https://raw.githubusercontent.com/${GITHUB_REPO}/80e2a3006ff1ad697b64481a38e38e1fe809c386/install.sh}"

info()  { echo "[INFO] $*"; }
warn()  { echo "[WARN] $*" >&2; }
error() { echo "[ERROR] $*" >&2; }
die()   { error "$*"; exit 1; }

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  die "此 snippet 需要以 root 运行。"
fi

for cmd in systemctl curl tar flock ss; do
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    die "未检测到 ${cmd}；该 snippet 依赖标准 systemd + curl/tar 环境。"
  fi
done

if ! [[ "${LISTEN_PORT}" =~ ^[0-9]+$ ]] || (( LISTEN_PORT < 1025 || LISTEN_PORT > 65535 )); then
  die "LISTEN_PORT 无效（需为 1025-65535）：${LISTEN_PORT}"
fi

exec 9>"${LOCK_FILE}"
flock -x 9
chown root:root "${LOCK_FILE}" || true
chmod 600 "${LOCK_FILE}" || true

# ---- 前置：Tailscale 必须在线并已分配 IPv4 ----
command -v tailscale >/dev/null 2>&1 \
  || die "未检测到 tailscale；请先完成 Tailscale 部署并 tailscale up。"

TS_IP="$(tailscale ip -4 2>/dev/null | head -1 || true)"
if ! [[ "${TS_IP}" =~ ^100\.([6-9][0-9]|1[0-2][0-9])\. ]]; then
  die "无法获取有效的 Tailscale IPv4（得到：'${TS_IP:-空}'）；请先 tailscale up 后重试。"
fi
info "Tailscale IPv4: ${TS_IP}"

# ---- 下载 fork 的 install.sh 并做来源自检 ----
tmp_installer="$(mktemp /tmp/xui-bridge-install.XXXXXX.sh)"
trap 'rm -f "${tmp_installer}"' EXIT

info "下载安装脚本: ${INSTALL_SH_URL}"
if [[ -n "${INSTALLER_LOCAL:-}" ]]; then
  [[ -f "$INSTALLER_LOCAL" ]] || die "验收安装器不存在；未发布覆盖版本须先复制完整验收bundle，不会退回latest"
  cp "$INSTALLER_LOCAL" "$tmp_installer"
else
  [[ "$INSTALL_SH_URL" != */main/* ]] || die "请提供固定提交的安装器URL和SHA256"
  curl -fsSL --connect-timeout 10 --max-time 60 "$INSTALL_SH_URL" -o "$tmp_installer" || die "下载失败"
fi
[[ "${INSTALLER_SHA256:-}" =~ ^[0-9a-f]{64}$ ]] || die "必须填写安装器SHA256"
[[ "$(sha256sum "$tmp_installer" | awk '{print $1}')" == "$INSTALLER_SHA256" ]] || die "安装器摘要不符"
[[ -n "${BRIDGE_VERSION:-}" ]] || die "必须指定固定版本"
export BRIDGE_VERSION BRIDGE_LOCAL_BINARY BRIDGE_LOCAL_SHA256

# 自检：脚本内 GITHUB_REPO 必须指向本 fork，否则二进制会从别处下载
if ! grep -q "^GITHUB_REPO=\"${GITHUB_REPO}\"" "${tmp_installer}"; then
  die "安装脚本来源自检失败：其 GITHUB_REPO 不是 ${GITHUB_REPO}，拒绝执行。"
fi
info "安装脚本来源自检通过（GITHUB_REPO=${GITHUB_REPO}）"

upgrade_mode="no"
[[ -x "${BIN_PATH}" ]] && upgrade_mode="yes"

# ---- 执行安装 / 升级 ----
# stdin=/dev/null：install.sh 内所有 [y/N] 交互（如 UFW 放行 8787）自动按"否"处理，
# 保证 8787 不进防火墙放行列表；安装失败时 install.sh 自带诊断输出。
info "开始安装（upgrade=${upgrade_mode}），过程输出如下："
if ! bash "${tmp_installer}" install </dev/null; then
  die "install.sh 执行失败；请按其上方日志排查（journalctl -u ${SERVICE_NAME}）。"
fi

# ---- 绑定 Tailscale IP（systemd drop-in，幂等覆盖）----
# 与 install.sh 的 change-listen-addr 使用同一 drop-in 文件，helper 兼容。
# After/Wants tailscaled：开机等 Tailscale 起来；IP 未就绪导致绑定失败时
# 由基础 unit 的 Restart=on-failure 每 5s 重试，直至成功。
dropin_prev=""
[[ -f "${DROPIN_FILE}" ]] && dropin_prev="$(cat "${DROPIN_FILE}")"

mkdir -p "${DROPIN_DIR}"
cat > "${DROPIN_FILE}" <<EOF
# Managed by Termius snippet: 203. 部署 xboard-xui-bridge
[Unit]
After=tailscaled.service
Wants=tailscaled.service

[Service]
Environment=BRIDGE_LISTEN_ADDR=${TS_IP}:${LISTEN_PORT}
EOF
chmod 644 "${DROPIN_FILE}"

rollback_listen() {
  warn "监听地址切换失败，正在回滚 drop-in..."
  if [[ -n "${dropin_prev}" ]]; then
    printf '%s\n' "${dropin_prev}" > "${DROPIN_FILE}"
  else
    rm -f "${DROPIN_FILE}"
  fi
  systemctl daemon-reload
  systemctl restart "${SERVICE_NAME}" >/dev/null 2>&1 || true
  warn "已回滚为原监听配置（默认 :${LISTEN_PORT} 绑全网卡；UFW 未放行该端口，公网仍不可达）。"
}

systemctl daemon-reload
if ! systemctl restart "${SERVICE_NAME}"; then
  rollback_listen
  die "服务以新监听地址重启失败，已回滚。"
fi

sleep 2

if ! systemctl is-active --quiet "${SERVICE_NAME}"; then
  rollback_listen
  die "服务未进入 active，已回滚；请 journalctl -u ${SERVICE_NAME} -n 50 排查。"
fi

# ---- 验收：监听地址与健康检查 ----
listen_line="$(ss -H -tln 2>/dev/null | awk -v p=":${LISTEN_PORT}" '$4 ~ p"$" {print $4}' | head -1)"
if [[ "${listen_line}" != "${TS_IP}:${LISTEN_PORT}" ]]; then
  rollback_listen
  die "监听验收失败：期望 ${TS_IP}:${LISTEN_PORT}，实际 '${listen_line:-未监听}'，已回滚。"
fi

health="fail"
for _ in 1 2 3 4 5; do
  if curl -fsS --max-time 5 "http://${TS_IP}:${LISTEN_PORT}/api/health" >/dev/null 2>&1; then
    health="ok"
    break
  fi
  sleep 1
done
if [[ "${health}" != "ok" ]]; then
  rollback_listen
  die "健康检查 /api/health 未通过，已回滚。"
fi

bridge_version="$("${BIN_PATH}" version 2>/dev/null | awk '{print $2}' | head -1 || echo unknown)"

initial_pwd_note="（升级安装，沿用旧密码）"
if [[ "${upgrade_mode}" == "no" && -f "${DATA_DIR}/initial_password.txt" ]]; then
  initial_pwd_note="$(cat "${DATA_DIR}/initial_password.txt" 2>/dev/null || echo '读取失败')"
fi

echo ""
echo "========== 203. xboard-xui-bridge 部署摘要 =========="
echo "安装来源: github.com/${GITHUB_REPO}（自有 fork Release + SHA256 校验）"
echo "版本: ${bridge_version}"
echo "模式: $([[ "${upgrade_mode}" == "yes" ]] && echo 升级 || echo 全新安装)"
echo "监听地址: ${TS_IP}:${LISTEN_PORT}（仅 Tailscale 内网可达）"
echo "面板入口: http://${TS_IP}:${LISTEN_PORT}"
echo "健康检查: ${health}"
echo "默认用户名: admin"
echo "初始密码: ${initial_pwd_note}"
echo "drop-in: ${DROPIN_FILE}"
echo "数据目录: ${DATA_DIR}"
echo ""
echo "[INFO] 首次登录后请立即修改密码，并删除 ${DATA_DIR}/initial_password.txt"
echo "[INFO] UFW 未放行 ${LISTEN_PORT}，公网不可达——这是预期行为，请勿手动放行。"
echo "[INFO] 面板内继续配置 Xboard 通讯密钥与 3x-ui API Token 完成桥接。"
echo "====================================================="
__TERMIUS_BASH__

  chmod 700 "$_tmp_script"

  if [ "$(id -u 2>/dev/null || echo 1)" = "0" ]; then
    env \
      BRIDGE_VERSION="${BRIDGE_VERSION}" \
      BRIDGE_LOCAL_BINARY="${BRIDGE_LOCAL_BINARY}" \
      BRIDGE_LOCAL_SHA256="${BRIDGE_LOCAL_SHA256}" \
      INSTALLER_LOCAL="${INSTALLER_LOCAL}" \
      INSTALLER_SHA256="${INSTALLER_SHA256}" \
      INSTALL_SH_URL="${INSTALL_SH_URL:-}" \
      GITHUB_REPO="${GITHUB_REPO}" \
      LISTEN_PORT="${LISTEN_PORT}" \
      bash "$_tmp_script"
  else
    sudo env \
      BRIDGE_VERSION="${BRIDGE_VERSION}" \
      BRIDGE_LOCAL_BINARY="${BRIDGE_LOCAL_BINARY}" \
      BRIDGE_LOCAL_SHA256="${BRIDGE_LOCAL_SHA256}" \
      INSTALLER_LOCAL="${INSTALLER_LOCAL}" \
      INSTALLER_SHA256="${INSTALLER_SHA256}" \
      INSTALL_SH_URL="${INSTALL_SH_URL:-}" \
      GITHUB_REPO="${GITHUB_REPO}" \
      LISTEN_PORT="${LISTEN_PORT}" \
      bash "$_tmp_script"
  fi

  _rc=$?
  rm -f "$_tmp_script"

else
  echo "[ERROR] 无法创建临时脚本，未执行任何操作。" >&2
  _rc=1
fi

echo "[INFO] 临时脚本退出码: $_rc"

if [ "$_rc" -eq 0 ]; then
  echo "[INFO] 203. xboard-xui-bridge 部署执行完成。"
else
  echo "[ERROR] 203. xboard-xui-bridge 部署执行失败；当前 Termius SSH 会话已保留。"
fi

true

# ============================================================================
# xboard-xui-bridge 常用运维命令速查（仅注释，不执行）
# ----------------------------------------------------------------------------
# 【服务管理（xui-bridge helper 已随安装注册到 $PATH）】
#   xui-bridge status                # systemctl 状态
#   xui-bridge log [-e|-w] [N]       # 最近 N 行日志，可按级别筛选
#   xui-bridge follow                # 实时跟踪日志
#   xui-bridge restart               # 重启服务
#   xui-bridge backup                # 备份数据库（保留最近 5 份）
#
# 【升级（从自有 fork 的最新 Release）】
#   xui-bridge install               # 自动备份旧二进制+数据库后升级
#   ← 升级前需先在 fork 打新 tag（如 v0.11.0）让 CI 出新 Release；
#     本 snippet 重跑一遍效果等同，且会顺带校验监听绑定
#
# 【密码】
#   xui-bridge reset-password                          # 忘密码兜底（本地 shell 重置）
#   cat /usr/local/xboard-xui-bridge/data/initial_password.txt   # 初始密码（改密后请删）
#
# 【监听地址】
#   cat /etc/systemd/system/xboard-xui-bridge.service.d/listen.conf   # 当前绑定
#   xui-bridge change-listen-addr    # 交互式修改（会覆盖 203 写的 drop-in）
#   ← Tailscale IP 变化后重跑 203 即可自动重绑
#
# 【访问面板】
#   本机浏览器直接打开 http://<TailscaleIP>:8787（设备需在同一 tailnet）
#   或 SSH 转发：ssh -L 8787:<TailscaleIP>:8787 <vps> 后打开 http://127.0.0.1:8787
#
# 【卸载】
#   xui-bridge uninstall             # 卸载但保留 data/
#   xui-bridge purge                 # 彻底清理（含数据，不可恢复）
# ============================================================================
