Done. The full document and response are now saved:

| File | Size | Status |
|------|------|--------|
| `expansion-v126-ai-native-pipeline-near-data-compute-data-lineage.md` | 25.6 KB / 507 lines | ✅ Saved |
| `expansion-v126-ai-native-pipeline-near-data-compute-data-lineage.out.md` | 8.8 KB / ~190 lines | ✅ Saved |

The response (`.out.md`) includes:

1. **All 5 directions: anchor verification** — every code claim checked against actual codebase, all confirmed ✅. Two corrections noted:
   - `Chunker.Chunk()` returns `[]string` not `[]Chunk` (no metadata on chunks)
   - Embeddings have no independent ID — provenance tracing should stop at chunk level

2. **Cross-direction dependency chain** uncovered: 方向一（chunk ID stability）→ 方向三（embed model tagging）→ 方向四（model-aware provenance）

3. **Key conflict identified**: 方向三 × 方向五 — semantic dedup across different embedder models is meaningless without model isolation

4. **Implementation risks per direction** with concrete mitigation — 方向二's infinite trigger loop flagged as **critical**

5. **3 unmentioned cross-points** with high potential: chunk-level dedup (方向一 × 方向五), programmable AI pipeline (方向二 × 方向三), model audit chain (方向三 × 方向四)

6. **MVP scope estimate** for each direction (2 days to 6 weeks)
