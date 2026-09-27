# Production semantic indexing coverage

The engineering-graph service invokes the isolated semantic producer for default
branch pushes when `operatorConfigs.semanticProducer.secretName` names an operator
configuration. The mounted JSON pins the image digest, architecture, resource
limits and deadline. Helm grants this controller only namespace/pod/exec and
network-policy operations; it has no Secret, ConfigMap or PVC access.

| Language   | Qualified producer | Observed relationship                                                     |
| ---------- | ------------------ | ------------------------------------------------------------------------- |
| Go         | SCIP and Go LSP    | Cross-file definition/reference; production caller-file `depends_on` edge |
| TypeScript | SCIP               | Cross-file definition/reference; production caller-file `depends_on` edge |
| Python     | SCIP               | Cross-file definition/reference; production caller-file `depends_on` edge |

September 27's fixture pushes reached the actual production graph API. A Go
feature-branch push replacing `Hello` with `FeatureOnly` did not replace the
default-branch symbol or introduce the feature symbol into that graph. An
unsupported Java fixture produced no semantic result; malformed Go was rejected
rather than reported as a complete snapshot.

This proves file-to-symbol dependency edges, not a complete function call graph.
`tested_by` attribution remains based on test-file naming. Semantic symbol
dependencies are explicitly unavailable in the graph RPC; the current REST
projection returns an empty dependency list in that case. Neither empty results
nor unsupported/incomplete indexing prove absence of references or dead code.

The pinned image and execution digest, fixture responses and observed branch
behavior are in [qualification evidence](../.procoder/evidence/intelligence-20260927.md).
Keep the image/configuration digest with each qualification. New languages or
dependency-resolution requirements need their own isolated producer and live
fixtures; the tested three-language matrix does not establish universal coverage.
