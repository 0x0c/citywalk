-- Extensions every later migration can assume are present: gen_random_uuid() backs the
-- identifiers CW-0003's schema assigns to messages, variants, and channels.
CREATE EXTENSION IF NOT EXISTS pgcrypto;
