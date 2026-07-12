I've now surveyed the landscape. There are **120+ design documents** in `docs/results/`, several directly security-relevant. Here are my top recommendations for a focused security review:

---

## Options for Security Review Target

### 🔴 Option A: **MCP Protocol Security** (`expansion-v67`)
**File:** `docs/results/expansion-v67-production-governance-mcp-security.out.arch.md`

MCP (Model Context Protocol) is a bidirectional JSON-RPC channel with tools like `list_files`, `read_file`, `write_file`, `delete_file`, `search`, and `chat`. This is a high-risk surface — it allows an LLM agent to perform arbitrary file operations. Security review would cover tool authorization scoping, prompt injection hardening, and the stdio vs HTTP transport security model.

### 🟠 Option B: **Security Benchmark & Config Gaps** (`expansion-v118-security-benchmark-config-gaps`)
**File:** `docs/results/expansion-v118-security-benchmark-config-gaps.out.arch.md`

A broad review of the auth layer (JWT/API key/SigV4), PublicAccessBlock, bucket-level security governance, ACL evaluation, and configuration validation. Covers the entire auth → tenant → rate-limit middleware chain and S3 protocol compliance.

### 🟡 Option C: **SSE/Encryption & Key Management** (`expansion-v117`)
**File:** `docs/results/expansion-v117-authentic-production-gaps.out.arch.md`

Reviews the SSE-C/KMS encryption pipeline, key rotation/re-wrap, the `SecretProvider` interface, and potential plaintext leakage in storage paths. Includes analysis of the OOM risk in the local SSE encrypt path (full memory loading of objects).

### 🟢 Option D: **AI Pipeline Security** (`expansion-v103-security-edge-foundations`)
**File:** `docs/results/expansion-v103-security-edge-foundations.out.arch.md`

Reviews the RAG pipeline: extractor → chunker → embedder → index → search → chat/agent. Covers PII detection/redaction, prompt injection in the chat/agent loop, budget controls, rate limiting, and data exfiltration via search results.

### 🔵 Option E: **Full-System Threat Model** (holistic)
I perform a STRIDE analysis across all subsystems using the existing `AGENTS.md` Feature Matrix as a threat surface map, plus targeted source code reading.

---

**Which would you like to focus on?** Or if you prefer, I can choose Option E (full-system STRIDE) since it gives the broadest coverage of adversarial production risks.
