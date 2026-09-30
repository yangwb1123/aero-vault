#!/bin/bash
# install-hooks.sh — 安装 Git pre-commit / pre-push hook 到本地仓库
set -euo pipefail

HOOK_DIR="$(git rev-parse --git-dir)/hooks"
HOOK_FILE="${HOOK_DIR}/pre-commit"

cat > "$HOOK_FILE" << 'HOOK'
#!/bin/bash
# pre-commit hook — 提交前运行 make accept（全量工程门禁）
set -euo pipefail

echo "=== pre-commit: 运行 python cli.py accept ==="
if ! python3 cli.py accept 2>&1; then
    echo "=== pre-commit: FAILED ==="
    echo "请修复上述问题后重新提交。"
    echo "可通过 --no-verify 跳过（不推荐）。"
    exit 1
fi
echo "=== pre-commit: PASSED ==="
HOOK

chmod +x "$HOOK_FILE"
echo "✓ pre-commit hook 已安装: $HOOK_FILE"

# ── pre-push：前端交付门禁 ───────────────────────────────────────────────
# 直接复制仓库内受版本控制的 githooks/pre-push（不内联副本，避免 hook 与
# 脚本漂移）：make web-check → make build → scripts/web-local-smoke.sh。
# 该 hook 依赖 node >= 22 与 Chrome/Chromium；缺失时 fail-closed。
PRE_PUSH_SRC="$(git rev-parse --show-toplevel)/githooks/pre-push"
PRE_PUSH_DST="${HOOK_DIR}/pre-push"

if [ -f "$PRE_PUSH_SRC" ]; then
    cp "$PRE_PUSH_SRC" "$PRE_PUSH_DST"
    chmod +x "$PRE_PUSH_DST"
    echo "✓ pre-push hook 已安装: $PRE_PUSH_DST"
else
    echo "✗ 未找到 $PRE_PUSH_SRC，跳过 pre-push hook 安装" >&2
    exit 1
fi
