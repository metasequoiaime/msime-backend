-- Curated candidate-window skin packages in the msime-skins format (skin.toml plus assets), served read-only through /v1/skins next to the embedded builtins and skins_root. Operators write them with scripts/candidate_skins_seed.py; clients never do. The service re-validates every manifest on read with the client loader's rules (internal/skins/client.go), so these constraints are a floor, not the whole contract.
CREATE TABLE IF NOT EXISTS candidate_skins (
 id text PRIMARY KEY CHECK(id ~ '^[a-z0-9][a-z0-9._-]{0,63}$' AND id NOT IN ('fluent','wechat','graphite','willow_green','system','shuishan','light','paper','night','ink','custom')),
 manifest bytea NOT NULL CHECK(octet_length(manifest) BETWEEN 1 AND 65536),
 manifest_sha256 text GENERATED ALWAYS AS (encode(sha256(manifest),'hex')) STORED,
 published boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
-- One row per package file other than skin.toml. Size and digest are generated from the bytes so they can never disagree with what is served; the media type follows from the extension.
CREATE TABLE IF NOT EXISTS candidate_skin_resources (
 skin_id text NOT NULL REFERENCES candidate_skins(id) ON DELETE CASCADE,
 path text NOT NULL CHECK(length(path) <= 256 AND path ~ '^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$' AND path !~ '(^|/)\.\.?(/|$)' AND lower(path) ~ '\.(css|png|jpe?g|gif|webp|svg|ico|bmp|avif|woff2?|ttf|otf)$'),
 bytes bytea NOT NULL CHECK(octet_length(bytes) <= 4194304),
 size integer GENERATED ALWAYS AS (octet_length(bytes)) STORED,
 sha256 text GENERATED ALWAYS AS (encode(sha256(bytes),'hex')) STORED,
 PRIMARY KEY(skin_id,path)
);
