-- What a named /v1/admin token may do, from the operators' own permission
-- catalogue (internal/rbac). A route outside the list answers 403, and the
-- refusal is in the token's request log like every other call.
--
-- NULL is full access: the tokens minted before permissions existed keep
-- working as they did, and the panel shows them as full-access tokens so an
-- owner can replace them with scoped ones. A token minted now names its
-- permissions, or asks for full access explicitly.
ALTER TABLE api_tokens
  ADD COLUMN permissions text[],
  ADD CONSTRAINT api_tokens_permissions_bounded CHECK (permissions IS NULL OR cardinality(permissions) <= 64);
