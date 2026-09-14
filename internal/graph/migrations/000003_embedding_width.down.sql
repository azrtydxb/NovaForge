DELETE FROM code_chunks;

DROP INDEX IF EXISTS code_chunks_embedding_idx;

ALTER TABLE code_chunks ALTER COLUMN embedding TYPE public.vector(768);

CREATE INDEX code_chunks_embedding_idx ON code_chunks USING hnsw (embedding public.vector_cosine_ops);
