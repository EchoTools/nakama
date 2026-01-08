/*
 * Copyright 2026 The Nakama Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

-- +migrate Up
-- Migrate EVR profile data from user metadata to storage objects.
-- This migration copies user metadata to the EVRProfile storage collection,
-- but only for users who don't already have an EVRProfile storage object.
-- This ensures we don't clobber existing storage objects.

INSERT INTO storage (collection, key, user_id, value, version, read, write, create_time, update_time)
SELECT 
    'EVRProfile' AS collection,
    'profile' AS key,
    u.id AS user_id,
    u.metadata AS value,
    md5(u.metadata::text) AS version,
    0 AS read,  -- STORAGE_PERMISSION_NO_READ
    0 AS write, -- STORAGE_PERMISSION_NO_WRITE
    now() AS create_time,
    now() AS update_time
FROM users u
WHERE u.id != '00000000-0000-0000-0000-000000000000'  -- Exclude system user
  AND u.metadata != '{}'::jsonb  -- Only migrate users with metadata
  AND NOT EXISTS (
    SELECT 1 FROM storage s 
    WHERE s.collection = 'EVRProfile' 
      AND s.key = 'profile' 
      AND s.user_id = u.id
  );

-- +migrate Down
-- Remove EVRProfile storage objects that were created by this migration.
-- Note: This will remove ALL EVRProfile storage objects, not just migrated ones.
-- Use with caution.
DELETE FROM storage 
WHERE collection = 'EVRProfile' 
  AND key = 'profile';
