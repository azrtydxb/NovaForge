-- The embedding model the gateway serves (bge-m3) answers with 1024
-- dimensions; the column stored 768, so no chunk could ever be written.
--
-- Chunks are derived data: every one is rebuilt from the repository by the
-- next push that touches its file. None can have been stored at the old width
-- by the real model, so clearing the table loses nothing that existed.
DELETE FROM code_chunks;

DROP INDEX IF EXISTS code_chunks_embedding_idx;

ALTER TABLE code_chunks ALTER COLUMN embedding TYPE public.vector(1024);

CREATE INDEX code_chunks_embedding_idx ON code_chunks USING hnsw (embedding public.vector_cosine_ops);
