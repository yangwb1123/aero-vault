#!/bin/bash
# install-hooks.sh — 安装 Git pre-commit hook 到本地仓库
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
