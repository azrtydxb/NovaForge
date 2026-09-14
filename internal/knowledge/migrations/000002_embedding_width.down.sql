UPDATE knowledge_entries SET embedding = NULL;

DROP INDEX IF EXISTS knowledge_entries_embedding_idx;

ALTER TABLE knowledge_entries ALTER COLUMN embedding TYPE public.vector(768);

CREATE INDEX knowledge_entries_embedding_idx ON knowledge_entries USING hnsw (embedding public.vector_cosine_ops);
