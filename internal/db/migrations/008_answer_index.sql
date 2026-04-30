ALTER TABLE word_list
ADD COLUMN IF NOT EXISTS answer_index INTEGER;

WITH ranked_answers AS (
    SELECT id, ROW_NUMBER() OVER (ORDER BY id) AS seq
    FROM word_list
    WHERE is_answer = true
)
UPDATE word_list wl
SET answer_index = ranked_answers.seq
FROM ranked_answers
WHERE wl.id = ranked_answers.id
  AND wl.answer_index IS NULL;

UPDATE word_list
SET answer_index = NULL
WHERE is_answer = false;

CREATE UNIQUE INDEX IF NOT EXISTS idx_word_list_answer_index
ON word_list (answer_index)
WHERE answer_index IS NOT NULL;
