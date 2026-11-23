#!/bin/sh
# Termius Snippet 名称：204. xboard & xbridge 定时备份（安装器）
# 用途：生成常驻备份脚本 /usr/local/sbin/xboard-xbridge-backup.sh，
#       并通过 /etc/cron.d/xboard-xbridge-backup 安排每天凌晨 4 点执行。
#
# 备份特性（为什么这样设计）：
# - Xboard 支持 SQLite/MySQL 两种备份路径：SQLite 用在线备份 API；MySQL 从同一
#   Compose 项目的 mysql 服务执行 --single-transaction 逻辑备份。两者都归档
#   .env（含 APP_KEY）+ Compose/数据库密钥 + storage/theme + plugins。
# - all-in-one 内置 Redis 会先执行 SAVE（容器已停止时使用关停时生成的 RDB），
#   再复制并用 redis-check-rdb 校验，避免清除 named volume 后丢失会话、队列和统计缓冲。
# - xboard-xui-bridge 是 systemd 服务，数据为 /usr/local/xboard-xui-bridge/data/bridge.db，
#   内含全部 3x-ui 令牌与 Xboard 通讯密钥（WAL 模式）。
# - SQLite/bridge 均可能为 WAL：不能直接 cp，使用 `sqlite3 .backup`。
# - bridge.db 的 source/seq/outbox/positions 与 XBoard 流量回执必须共同保留；独立回灌需先对账。
# - MySQL 使用 InnoDB 事务一致性 dump；不锁业务表，但 DDL 变更期间不要运行备份。
# - 每次产出单个归档 xboard-xbridge-backup-<ts>.tar.gz，内含 xboard/ 与 bridge/ 两个顶层目录。
#
# 说明：
# - 本文件仅在安装阶段以 launcher + 临时脚本模式执行，避免安装报错终止当前 Termius 会话
# - 实际每日备份逻辑运行于 cron 触发的独立子进程，不受此模式约束
# - 使用 flock 防止安装阶段重复并发执行
# - PostgreSQL 仍不在本脚本覆盖范围；检测到会失败，避免产生缺数据库的假备份

# 可长期保存到 Termius Snippet 顶部的可调参数（留空即用默认）：
XBOARD_DIR="/lzy/Xboard"
BRIDGE_DATA_DIR="/usr/local/xboard-xui-bridge/data"
BACKUP_DIR="/lzy/backup/xboard-xbridge"
RETENTION_DAYS="3"
CRON_TIME="15 4 * * *"

_tmp_script=""
_rc=1

if ! command -v bash >/dev/null 2>&1; then
  echo "[ERROR] 未检测到 bash，请先安装 bash。" >&2

elif [ "$(id -u 2>/dev/null || echo 1)" != "0" ] && ! command -v sudo >/dev/null 2>&1; then
  echo "[ERROR] 当前用户不是 root，且未检测到 sudo；请切换 root 后运行。" >&2

elif _tmp_script="$(mktemp /tmp/termius-xboard-xbridge-backup-install.XXXXXX.sh)"; then
  cat > "$_tmp_script" <<'__TERMIUS_BASH__'
#!/usr/bin/env bash
set -euo pipefail
umask 077

export PATH="/usr/sbin:/usr/bin:/sbin:/bin:${PATH:-}"

XBOARD_DIR="${XBOARD_DIR:-/lzy/Xboard}"
BRIDGE_DATA_DIR="${BRIDGE_DATA_DIR:-/usr/local/xboard-xui-bridge/data}"
BACKUP_DIR="${BACKUP_DIR:-/lzy/backup/xboard-xbridge}"
RETENTION_DAYS="${RETENTION_DAYS:-7}"
CRON_TIME="${CRON_TIME:-0 4 * * *}"

BACKUP_SCRIPT="/usr/local/sbin/xboard-xbridge-backup.sh"
CRON_FILE="/etc/cron.d/xboard-xbridge-backup"
LOG_FILE="/var/log/xboard-xbridge-backup.log"
LOCK_FILE="/var/lock/termius-xboard-xbridge-backup-install.lock"

info()  { echo "[INFO] $*"; }
warn()  { echo "[WARN] $*" >&2; }
error() { echo "[ERROR] $*" >&2; }
die()   { error "$*"; exit 1; }

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  die "此 snippet 需要以 root 运行。"
fi

for cmd in apt-get systemctl mktemp mv chmod chown flock; do
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    die "未检测到 ${cmd}；该 snippet 仅支持 Debian + systemd。"
  fi
done

# 校验保留天数与 cron 时间基本形态
[[ "${RETENTION_DAYS}" =~ ^[0-9]+$ ]] || die "RETENTION_DAYS 必须为非负整数：${RETENTION_DAYS}"
if [[ ! "${CRON_TIME}" =~ ^[0-9*,/-]+([[:space:]]+[0-9*,/-]+){4}$ ]]; then
  die "CRON_TIME 形态非法（需 5 段，如 '0 4 * * *'）：${CRON_TIME}"
fi

exec 9>"${LOCK_FILE}"
if ! flock -n 9; then
  die "检测到另一个安装实例正在运行，本次执行已跳过。"
fi

# ---- 前置：Xboard 必须按预期路径部署 ----
if [[ ! -d "${XBOARD_DIR}" ]]; then
  die "未找到 Xboard 目录 ${XBOARD_DIR}；请确认部署路径或在 snippet 顶部改 XBOARD_DIR。"
fi
if [[ ! -f "${XBOARD_DIR}/.env" ]]; then
  die "未找到 ${XBOARD_DIR}/.env；请确认这是 Xboard 部署根目录。"
fi

# bridge 可选：不存在只提示，仍安装（本机可能只跑 Xboard）
if [[ ! -f "${BRIDGE_DATA_DIR}/bridge.db" ]]; then
  warn "未找到 ${BRIDGE_DATA_DIR}/bridge.db；备份将只覆盖 Xboard（bridge 未部署或路径不同）。"
fi

# ---- 依赖：sqlite3（Xboard/bridge 在线备份）与 cron ----
if ! command -v sqlite3 >/dev/null 2>&1 || ! command -v crontab >/dev/null 2>&1; then
  info "缺少 sqlite3 / cron，正在通过 apt-get 安装..."
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y sqlite3 cron
fi
command -v sqlite3 >/dev/null 2>&1 || die "安装后仍未检测到 sqlite3 命令。"
command -v crontab >/dev/null 2>&1 || die "安装后仍未检测到 crontab 命令。"
command -v docker >/dev/null 2>&1 || die "未检测到 docker；Xboard 备份需要读取 Compose 部署。"
docker compose version >/dev/null 2>&1 || die "Docker Compose v2 不可用（需 docker compose）。"

systemctl enable --now cron >/dev/null 2>&1 || die "cron 服务启用失败。"

# ---- 写入常驻备份脚本 ----
# 配置块用展开式 heredoc（把安装期解析到的路径固化进脚本），逻辑块用字面式 heredoc。
tmp_backup_script="$(mktemp)"
cat > "${tmp_backup_script}" <<EOF
#!/usr/bin/env bash
# 由 Termius snippet「204. xboard & xbridge 定时备份」生成，请勿手改路径以外内容。
XBOARD_DIR="${XBOARD_DIR}"
BRIDGE_DATA_DIR="${BRIDGE_DATA_DIR}"
BACKUP_DIR="${BACKUP_DIR}"
RETENTION_DAYS="${RETENTION_DAYS}"
BACKUP_FORMAT_VERSION="2"
EOF

cat >> "${tmp_backup_script}" <<'__XB_BACKUP_SH__'
set -euo pipefail
umask 077

export PATH="/usr/sbin:/usr/bin:/sbin:/bin:${PATH:-}"

BRIDGE_DB="${BRIDGE_DATA_DIR}/bridge.db"
ENV_FILE="${XBOARD_DIR}/.env"
LOCK_FILE="/var/lock/xboard-xbridge-backup.lock"

log()   { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"; }
warn()  { echo "[$(date '+%Y-%m-%d %H:%M:%S')] [WARN] $*" >&2; }
error() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] [ERROR] $*" >&2; }
die()   { error "$*"; exit 1; }

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  die "此脚本需要以 root 运行。"
fi

for cmd in tar sqlite3 find du df mkdir date mv cp flock awk sed tr hostname docker grep head tail sha256sum sort xargs basename; do
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    die "未检测到必需命令: ${cmd}"
  fi
done

exec 9>"${LOCK_FILE}"
if ! flock -n 9; then
  die "检测到另一个备份实例正在运行，本次执行已跳过。"
fi

# ---- 定位 Xboard 数据库 ----
[[ -d "${XBOARD_DIR}" ]] || die "未找到 Xboard 目录 ${XBOARD_DIR}，备份中止。"
[[ -f "${ENV_FILE}"  ]] || die "未找到 ${ENV_FILE}，备份中止。"

# 从 .env 读取 KEY=VALUE（取最后一次出现，去除首尾引号）
get_env() {
  sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*//p" "${ENV_FILE}" \
    | tail -1 | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'$//"
}

DB_CONNECTION="$(get_env DB_CONNECTION)"; DB_CONNECTION="${DB_CONNECTION:-sqlite}"
DB_DATABASE="$(get_env DB_DATABASE)"
DB_USERNAME="$(get_env DB_USERNAME)"
DB_PASSWORD="$(get_env DB_PASSWORD)"
COMPOSE_VALUE="$(get_env COMPOSE_FILE)"

COMPOSE_FILES=()
if [[ -n "${COMPOSE_VALUE}" ]]; then
  IFS=':' read -r -a COMPOSE_FILES <<<"${COMPOSE_VALUE}"
else
  for compose_file in compose.yaml compose.yml docker-compose.yaml docker-compose.yml; do
    [[ -f "${XBOARD_DIR}/${compose_file}" ]] && COMPOSE_FILES+=("${compose_file}")
  done
  [[ ${#COMPOSE_FILES[@]} -eq 1 ]] || die "未设置 COMPOSE_FILE，且无法唯一确定基础 Compose 文件。"
fi
for compose_file in "${COMPOSE_FILES[@]}"; do
  [[ "${compose_file}" =~ ^[A-Za-z0-9._/-]+$ && "${compose_file}" != *..* ]] \
    || die "COMPOSE_FILE 包含不安全路径：${compose_file}"
  [[ -f "${XBOARD_DIR}/${compose_file}" ]] || die "Compose 文件不存在：${compose_file}"
done
[[ -f "${XBOARD_DIR}/compose.selfhost.override.yaml" ]] && COMPOSE_FILES+=("compose.selfhost.override.yaml")

mysql_dc() { (cd "${XBOARD_DIR}" && docker compose "$@"); }

SQLITE_SRC=""
MYSQL_SIZE_MB=""
case "${DB_CONNECTION}" in
  sqlite)
    if [[ -z "${DB_DATABASE}" ]]; then
      SQLITE_SRC="${XBOARD_DIR}/.docker/.data/database.sqlite"
    elif [[ "${DB_DATABASE}" == /* ]]; then
      SQLITE_SRC="${DB_DATABASE}"
    else
      SQLITE_SRC="${XBOARD_DIR}/${DB_DATABASE}"
    fi
    [[ -f "${SQLITE_SRC}" ]] || die "未找到 Xboard SQLite：${SQLITE_SRC}。"
    ;;
  mysql)
    [[ -n "${DB_DATABASE}" && -n "${DB_USERNAME}" ]] || die "MySQL 的 DB_DATABASE/DB_USERNAME 为空。"
    mysql_dc config --services | grep -qx mysql || die "Compose 中没有 mysql 服务；本脚本只覆盖 205 方案的同 Compose MySQL。"
    mysql_dc exec -T mysql sh -ec 'test -r /run/secrets/xboard_mysql_root_password' \
      || die "mysql 容器缺少 root secret；请确认 compose.mysql.yaml 来自 205 snippet。"
    MYSQL_SIZE_MB="$(mysql_dc exec -T mysql sh -ec '
      export MYSQL_PWD="$(cat /run/secrets/xboard_mysql_root_password)"
      exec mysql -h 127.0.0.1 -uroot -Nse "SELECT GREATEST(1, CEIL(COALESCE(SUM(data_length+index_length),0)/1024/1024)) FROM information_schema.tables WHERE table_schema=DATABASE()" "$1"
    ' sh "${DB_DATABASE}" | tr -d '\r[:space:]')"
    [[ "${MYSQL_SIZE_MB}" =~ ^[0-9]+$ ]] || die "无法取得 MySQL 数据库大小：${MYSQL_SIZE_MB:-无返回}"
    ;;
  *)
    die "检测到 DB_CONNECTION=${DB_CONNECTION}；仅支持 sqlite/mysql，已中止以避免不完整备份。"
    ;;
esac

have_bridge="no"
if [[ -f "${BRIDGE_DB}" ]]; then
  have_bridge="yes"
else
  warn "未找到 ${BRIDGE_DB}，本次仅备份 Xboard。"
fi

mkdir -p "${BACKUP_DIR}"

# ---- 空间预检（暂存目录 + 压缩归档，粗估需 2x 源大小）----
sum_mb=0
add_size() {
  [[ -e "$1" ]] || return 0
  local m
  m="$(du -sm "$1" 2>/dev/null | awk '{print $1}')"
  [[ -n "${m}" ]] && sum_mb=$(( sum_mb + m ))
}
if [[ "${DB_CONNECTION}" == sqlite ]]; then
  add_size "${SQLITE_SRC}"
else
  sum_mb=$(( sum_mb + MYSQL_SIZE_MB ))
fi
add_size "${ENV_FILE}"
add_size "${XBOARD_DIR}/storage/theme"
add_size "${XBOARD_DIR}/plugins"
for compose_file in "${COMPOSE_FILES[@]}"; do
  add_size "${XBOARD_DIR}/${compose_file}"
done
add_size "${XBOARD_DIR}/.docker/.secrets"
add_size "/usr/local/lib/xboard-selfhost"
[[ "${have_bridge}" == "yes" ]] && add_size "${BRIDGE_DB}"
need_mb=$(( sum_mb * 2 + 1 ))
avail_mb="$(df -m --output=avail "${BACKUP_DIR}" | tail -1 | tr -d ' ')"
if [[ "${avail_mb}" -lt "${need_mb}" ]]; then
  die "备份目录可用空间不足：需约 ${need_mb}M，仅剩 ${avail_mb}M。"
fi

timestamp="$(date +%Y%m%d-%H%M%S)"
final_archive="${BACKUP_DIR}/xboard-xbridge-backup-${timestamp}.tar.gz"
tmp_archive="${BACKUP_DIR}/.xboard-xbridge-backup-${timestamp}.tar.gz.tmp"
work="$(mktemp -d "${BACKUP_DIR}/.stage-${timestamp}-XXXXXX")"

cleanup() { rm -rf "${work}" "${tmp_archive}" 2>/dev/null || true; }
trap cleanup EXIT

# ---- SQLite 在线一致快照 ----
snapshot_sqlite() {  # $1=src $2=dst
  local src="$1" dst="$2" chk
  # .timeout：写锁并发时最多等 15s；.backup：在线备份 API，事务一致
  if ! sqlite3 "${src}" ".timeout 15000" ".backup '${dst}'"; then
    die "SQLite 在线备份失败：${src}"
  fi
  chk="$(sqlite3 "${dst}" 'PRAGMA integrity_check;' 2>/dev/null | head -1 || true)"
  if [[ "${chk}" != "ok" ]]; then
    die "备份完整性校验失败（${dst}）：${chk:-无返回}"
  fi
}

mkdir -p "${work}/xboard"
if [[ "${DB_CONNECTION}" == sqlite ]]; then
  log "快照 Xboard SQLite：${SQLITE_SRC}"
  snapshot_sqlite "${SQLITE_SRC}" "${work}/xboard/database.sqlite"
else
  log "导出 Xboard MySQL：${DB_DATABASE}"
  if ! mysql_dc exec -T mysql sh -ec '
    export MYSQL_PWD="$(cat /run/secrets/xboard_mysql_root_password)"
    exec mysqldump -h 127.0.0.1 -uroot --single-transaction --quick --hex-blob --no-tablespaces --set-gtid-purged=OFF --routines --events --triggers "$1"
  ' sh "${DB_DATABASE}" > "${work}/xboard/database.sql"; then
    die "MySQL mysqldump 失败。"
  fi
  [[ -s "${work}/xboard/database.sql" ]] || die "MySQL dump 为空。"
  grep -q '^-- Dump completed on ' "${work}/xboard/database.sql" \
    || die "MySQL dump 缺少完成标记。"
fi

# .env（含 APP_KEY，最关键）、主题、插件
cp -a "${ENV_FILE}" "${work}/xboard/.env"
if [[ -d "${XBOARD_DIR}/storage/theme" ]]; then
  cp -a "${XBOARD_DIR}/storage/theme" "${work}/xboard/storage-theme"
fi
if [[ -d "${XBOARD_DIR}/plugins" ]]; then
  cp -a "${XBOARD_DIR}/plugins" "${work}/xboard/plugins"
fi
mkdir -p "${work}/xboard/compose"
for compose_file in "${COMPOSE_FILES[@]}"; do
  cp -a "${XBOARD_DIR}/${compose_file}" "${work}/xboard/compose/$(basename "${compose_file}")"
done
if [[ -d "${XBOARD_DIR}/.docker/.secrets" ]]; then
  cp -a "${XBOARD_DIR}/.docker/.secrets" "${work}/xboard/docker-secrets"
fi
if [[ -d /usr/local/lib/xboard-selfhost ]]; then
  cp -a /usr/local/lib/xboard-selfhost "${work}/xboard/selfhost-scripts"
fi

if [[ "${have_bridge}" == "yes" ]]; then
  mkdir -p "${work}/bridge"
  log "快照 bridge 数据库：${BRIDGE_DB}"
  snapshot_sqlite "${BRIDGE_DB}" "${work}/bridge/bridge.db"
  [[ -x /usr/local/bin/xboard-xui-bridge ]] || die "桥接数据库存在但二进制缺失"
  cp -a /usr/local/bin/xboard-xui-bridge "${work}/bridge/xboard-xui-bridge"
  mkdir -p "${work}/bridge/systemd"
  cp -a /etc/systemd/system/xboard-xui-bridge.service "${work}/bridge/systemd/"
  if [[ -d /etc/systemd/system/xboard-xui-bridge.service.d ]]; then
    cp -a /etc/systemd/system/xboard-xui-bridge.service.d "${work}/bridge/systemd/"
  fi
  if [[ -f /usr/local/xboard-xui-bridge/install.sh ]]; then
    cp -a /usr/local/xboard-xui-bridge/install.sh "${work}/bridge/install.sh"
  fi
  if [[ -d /usr/local/lib/xbridge-selfhost ]]; then
    cp -a /usr/local/lib/xbridge-selfhost "${work}/bridge/selfhost"
  fi
  bridge_version="$(/usr/local/bin/xboard-xui-bridge version | awk '{print $2}')"
  bridge_binary_sha="$(sha256sum "${work}/bridge/xboard-xui-bridge" | awk '{print $1}')"
  printf 'version=%s\nbinary_sha256=%s\narchitecture=%s\n' "$bridge_version" "$bridge_binary_sha" "$(uname -m)" >"${work}/bridge/VERSION"
  chmod -R go-rwx "${work}/bridge"

fi

container_id="$(mysql_dc ps -a -q xboard 2>/dev/null || true)"
image_config="unknown"
image_id="unknown"
image_digest="unknown"
if [[ -n "${container_id}" ]]; then
  image_config="$(docker inspect --format '{{.Config.Image}}' "${container_id}" 2>/dev/null || echo unknown)"
  image_id="$(docker inspect --format '{{.Image}}' "${container_id}" 2>/dev/null || echo unknown)"
  image_digest="$(docker image inspect --format '{{index .RepoDigests 0}}' "${image_id}" 2>/dev/null || echo unknown)"
fi

# ---- 内置 Redis 一致性快照 ----
[[ -n "${container_id}" ]] || die "无法定位 Xboard 容器，不能备份内置 Redis。"
[[ "${image_config}" != "unknown" ]] || die "无法取得 Xboard 镜像引用，不能校验 Redis RDB。"
container_running="$(docker inspect --format '{{.State.Running}}' "${container_id}" 2>/dev/null || true)"
if [[ "${container_running}" == "true" ]]; then
  log "刷新内置 Redis RDB"
  docker exec "${container_id}" redis-cli -s /data/redis.sock SAVE |
    grep -qx 'OK' || die "Redis SAVE 失败。"
fi
mkdir -p "${work}/xboard/redis"
docker cp "${container_id}:/data/dump.rdb" "${work}/xboard/redis/dump.rdb" >/dev/null \
  || die "无法复制 Redis dump.rdb。"
[[ -s "${work}/xboard/redis/dump.rdb" ]] || die "Redis dump.rdb 为空。"
docker run --rm --entrypoint redis-check-rdb \
  -v "${work}/xboard/redis/dump.rdb:/tmp/xboard-dump.rdb:ro" \
  "${image_config}" /tmp/xboard-dump.rdb >/dev/null \
  || die "Redis RDB 完整性校验失败。"

# 说明文件，便于还原时对照来源
cat > "${work}/BACKUP-INFO.txt" <<META
created:        $(date '+%F %T %z')
format_version: ${BACKUP_FORMAT_VERSION}
host:           $(hostname 2>/dev/null || echo unknown)
xboard_dir:     ${XBOARD_DIR}
xboard_db_type: ${DB_CONNECTION}
xboard_db_src:  $([[ "${DB_CONNECTION}" == sqlite ]] && echo "${SQLITE_SRC}" || echo "compose:mysql/${DB_DATABASE}")
image_config:   ${image_config}
image_id:       ${image_id}
image_digest:   ${image_digest}
redis_snapshot: xboard/redis/dump.rdb
compose_files:  ${COMPOSE_FILES[*]}
bridge_db_src:  $([[ "${have_bridge}" == "yes" ]] && echo "${BRIDGE_DB}" || echo "(未备份)")
retention_days: ${RETENTION_DAYS}
note:           SQLite 用 .backup；MySQL 用 single-transaction mysqldump。还原前先停 Xboard。
META

(
  cd "${work}"
  find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS
  sha256sum -c SHA256SUMS >/dev/null
)

log "打包归档 ..."
if ! tar -czf "${tmp_archive}" -C "${work}" .; then
  die "tar 打包失败。"
fi

if ! tar -tzf "${tmp_archive}" >/dev/null 2>&1; then
  die "归档完整性校验失败：${tmp_archive}。"
fi

mv "${tmp_archive}" "${final_archive}"
chmod 600 "${final_archive}"

# 完整性校验通过后再清理旧备份
find "${BACKUP_DIR}" -maxdepth 1 -name 'xboard-xbridge-backup-*.tar.gz' -mtime "+${RETENTION_DAYS}" -delete

archive_size="$(du -h "${final_archive}" | awk '{print $1}')"
archive_sha256="$(sha256sum "${final_archive}" | awk '{print $1}')"
log "备份完成：${final_archive} (${archive_size})"
db_artifact="database.sqlite"; [[ "${DB_CONNECTION}" == mysql ]] && db_artifact="database.sql"
log "内容：xboard/${db_artifact} + redis/dump.rdb + .env + 生效 Compose/密钥 + selfhost 脚本 + storage-theme + plugins + SHA256SUMS$([[ "${have_bridge}" == "yes" ]] && echo ' ＋ bridge/bridge.db')"
log "保留策略：超过 ${RETENTION_DAYS} 天的旧备份已清理"
log "本次为在线一致性备份，Xboard 容器与 bridge 服务均未中断"
printf 'BACKUP_FORMAT_VERSION=%s\n' "${BACKUP_FORMAT_VERSION}"
printf 'BACKUP_ARCHIVE=%s\n' "${final_archive}"
printf 'BACKUP_SHA256=%s\n' "${archive_sha256}"
__XB_BACKUP_SH__

chmod 700 "${tmp_backup_script}"
chown root:root "${tmp_backup_script}"
mv "${tmp_backup_script}" "${BACKUP_SCRIPT}"

# ---- 写入 cron.d 条目 ----
tmp_cron="$(mktemp)"
cat > "${tmp_cron}" <<EOF
# 由 Termius snippet「204. xboard & xbridge 定时备份」生成
${CRON_TIME} root ${BACKUP_SCRIPT} >> ${LOG_FILE} 2>&1
EOF
chmod 644 "${tmp_cron}"
chown root:root "${tmp_cron}"
mv "${tmp_cron}" "${CRON_FILE}"

systemctl reload cron >/dev/null 2>&1 || systemctl restart cron >/dev/null 2>&1 || true

bridge_note="未部署（仅备份 Xboard）"
[[ -f "${BRIDGE_DATA_DIR}/bridge.db" ]] && bridge_note="${BRIDGE_DATA_DIR}/bridge.db"
installed_db_connection="$(sed -n 's/^[[:space:]]*DB_CONNECTION[[:space:]]*=[[:space:]]*//p' "${XBOARD_DIR}/.env" | tail -1 | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'$//")"
installed_db_connection="${installed_db_connection:-sqlite}"

echo ""
echo "========== 204. xboard & xbridge 定时备份 安装摘要 =========="
echo "备份脚本: ${BACKUP_SCRIPT}"
echo "cron 条目: ${CRON_FILE}（${CRON_TIME}，以 root 执行）"
echo "备份目录: ${BACKUP_DIR}"
echo "Xboard 源: ${XBOARD_DIR}（.env + ${installed_db_connection} + 生效 Compose/密钥 + selfhost 脚本 + storage/theme + plugins）"
echo "bridge 源: ${bridge_note}"
echo "备份方式: SQLite=.backup；MySQL=single-transaction mysqldump（在线一致性）"
echo "保留策略: 最近 ${RETENTION_DAYS} 天"
echo "日志文件: ${LOG_FILE}（未配置轮转）"
echo "手动测试: ${BACKUP_SCRIPT}"
echo "-------------------------------------------------------------"
echo "灾难恢复: 只使用经过会话绑定的 216-3；不要手工猜 Compose volume 或直接覆盖数据库。"
echo "重建用途: 手工执行本脚本得到的归档是 PRIMARY；把其绝对路径填入 216-0。"
echo "216-0 冻结后另做的归档是 GUARD 保底，不是 216-3 的默认恢复源。"
echo "============================================================="
echo "[INFO] 归档含 APP_KEY 与全部 3x-ui/Xboard 密钥，权限 600；拷离本机前请先加密。"
__TERMIUS_BASH__

  chmod 700 "$_tmp_script"

  if [ "$(id -u 2>/dev/null || echo 1)" = "0" ]; then
    env \
      XBOARD_DIR="${XBOARD_DIR}" \
      BRIDGE_DATA_DIR="${BRIDGE_DATA_DIR}" \
      BACKUP_DIR="${BACKUP_DIR}" \
      RETENTION_DAYS="${RETENTION_DAYS}" \
      CRON_TIME="${CRON_TIME}" \
      bash "$_tmp_script"
  else
    sudo env \
      XBOARD_DIR="${XBOARD_DIR}" \
      BRIDGE_DATA_DIR="${BRIDGE_DATA_DIR}" \
      BACKUP_DIR="${BACKUP_DIR}" \
      RETENTION_DAYS="${RETENTION_DAYS}" \
      CRON_TIME="${CRON_TIME}" \
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
  echo "[INFO] 204. xboard & xbridge 定时备份安装完成。"
else
  echo "[ERROR] 204. xboard & xbridge 定时备份安装失败；当前 Termius SSH 会话已保留。"
fi

true
