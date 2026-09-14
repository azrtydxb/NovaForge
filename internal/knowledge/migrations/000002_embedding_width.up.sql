-- The embedding model the gateway serves (bge-m3) answers with 1024
-- dimensions; the column stored 768, so no entry recorded with a real
-- embedding could have been written with one.
--
-- The embedding is nullable and derived from the entry's own text, and an
-- entry without one is still listed; only its vector is cleared, never the
-- decision it records.
UPDATE knowledge_entries SET embedding = NULL;

DROP INDEX IF EXISTS knowledge_entries_embedding_idx;

ALTER TABLE knowledge_entries ALTER COLUMN embedding TYPE public.vector(1024);

CREATE INDEX knowledge_entries_embedding_idx ON knowledge_entries USING hnsw (embedding public.vector_cosine_ops);
