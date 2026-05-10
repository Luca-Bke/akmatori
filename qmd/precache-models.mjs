// Build-time helper: pre-download the QMD embedding and reranker GGUFs so
// they're baked into the Docker image rather than fetched on first start.
// The generate model is intentionally skipped — Akmatori does not run the
// local query-expansion LLM path.
import {
  DEFAULT_EMBED_MODEL_URI,
  DEFAULT_RERANK_MODEL_URI,
  pullModels,
} from "/opt/qmd/dist/llm.js";

const models = [DEFAULT_EMBED_MODEL_URI, DEFAULT_RERANK_MODEL_URI];
console.log("QMD precache: pulling", models);

const results = await pullModels(models);
for (const r of results) {
  const mb = (r.sizeBytes / (1024 * 1024)).toFixed(1);
  console.log(`QMD precache: ${r.model} -> ${r.path} (${mb} MB)`);
}
