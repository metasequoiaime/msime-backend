-- 共享译文缓存。内容寻址，不带 user_id —— 缓存的是「这串文本译成什么」，跟谁请求过无关，所以这张表里没有任何能指回某个用户的列。写入前由服务端把关长度和字符集（见 internal/server/translation_cache.go），整段文本不进这里。
CREATE TABLE IF NOT EXISTS translation_cache (
    source_lang TEXT NOT NULL,
    target_lang TEXT NOT NULL,
    source_text TEXT NOT NULL,
    target_text TEXT NOT NULL,
    hit_count BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    used_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (source_lang, target_lang, source_text)
);

-- fillfactor 留出页内空间，让命中计数走 HOT 更新：不写索引、不留死元组，只改堆里那一行。读路径每次命中都要 UPDATE 一次，这一项直接决定它的代价。
ALTER TABLE translation_cache SET (fillfactor = 85);

-- 早期版本建过这个索引，它是 HOT 被阻断的直接原因（下面详述）。已经建了的库要把它去掉。
DROP INDEX IF EXISTS translation_cache_hits;

-- 注意：对**已经存在**的表，上面两条做完 HOT 仍然不会生效。fillfactor 只作用于新写的页，存量行躺在按
-- 100% 填满的页里，页内没有空间放新版本元组。生产上实测：只做前两步 HOT 仍是 0/36，重写表之后才变成
-- 21/21。已部署的库要补一条 `VACUUM FULL translation_cache;`（这张表很小，锁持有时间可忽略）。

-- 这里**故意不建** (hit_count, used_at) 索引。
--
-- 曾经建过，实测代价远大于收益：读路径的 `UPDATE ... SET hit_count=hit_count+1, used_at=now() RETURNING`
-- 改的正好是那两个索引列，HOT 优化被完全阻断 —— 生产上 n_tup_hot_upd/n_tup_upd = 0/36，每次缓存命中
-- 都要写新堆元组 + 两条索引项 + 一个死元组 + 对应 WAL，全都是为了一次「读」。而那个索引 idx_scan=1，
-- 应用从来没读过它。
--
-- 它当初是为「按 hit_count 挑高频条目、考虑收进出货词库」准备的。那个查询一个月跑一次，全表扫足够；
-- 为它在每次读上加一份索引写，方向是反的。真要做那件事时再临时建、用完删。
--
-- 也不要退而求其次建部分索引（例如 `WHERE hit_count > 0`）：hit_count 从 0 变 1 会改变索引成员关系，
-- 同样阻断 HOT，等于把这个问题原样搬回来。查询只用主键就够，这张表只需要主键一个索引。
