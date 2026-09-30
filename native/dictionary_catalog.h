#pragma once
#include "quanpin/quanpin_query.h"
#include "ranking.h"
#include "schemes/quanpin_scheme.h"
#include "schemes/shuangpin_scheme.h"

inline nlohmann::json
query_dictionary_catalog(const nlohmann::json &request,
                         const std::filesystem::path &root) {
  using namespace backend_ranking;
  const auto kind = request.at("kind").get<std::string>();
  auto text = request.at("text").get<std::string>();
  const int offset = request.value("offset", 0),
            limit = request.value("limit", 50);
  if (offset < 0 || offset > 1000000 || limit < 1 || limit > 200)
    return {{"error", "invalid_request"}};
  std::string table, key = "key", word = "value", where, order;
  if (kind == "pinyin") {
    QueryRequest query;
    if (request.value("scheme", std::string("pinyin")) == "shuangpin") {
      const auto &profile =
          GetShuangpinProfile(request.value("profile", std::string("xiaohe")));
      ShuangpinScheme input(profile);
      input.set_raw_input(text, text);
      query = input.build_request();
    } else {
      QuanpinScheme input;
      input.set_raw_input(text, text);
      query = input.build_request();
    }
    if (!query.valid || query.normalized_segmentation.empty())
      return {{"error", "invalid_request"}};
    text = query.normalized_segmentation;
    table = quanpin::build_table_name(quanpin::split_segments(text));
    // Table names are produced by Engine, never accepted as request fields.
    if (table.empty() || table.find('"') != std::string::npos)
      return {{"error", "invalid_request"}};
    where = "key=?1";
    order = "weight DESC,value";
  } else if (kind == "wubi") {
    table = "wubi86";
    where = "key LIKE ?1 || '%'";
    order = "weight DESC,key,value";
  } else if (kind == "quick") {
    table = "quick_parases";
    where = "key LIKE ?1 || '%'";
    order = "weight DESC,key,value";
  } else if (kind == "english") {
    table = "english_words";
    key = "word";
    word = "display";
    where = "word>=?1 AND word<?4";
    order = "CASE WHEN word=?1 THEN 0 ELSE 1 END,weight "
            "DESC,length(word),word,display";
  } else
    return {{"error", "invalid_request"}};
  sqlite3 *raw = nullptr;
  const auto path = root / (kind == "english" ? "english.db" : "msime.db");
  if (sqlite3_open_v2(path.string().c_str(), &raw, SQLITE_OPEN_READONLY,
                      nullptr) != SQLITE_OK) {
    if (raw)
      sqlite3_close(raw);
    return {{"error", "resources_unavailable"}};
  }
  DB db(raw, sqlite3_close);
  auto exists = prepare(
      db.get(), "SELECT 1 FROM sqlite_master WHERE type='table' AND name=?1");
  if (!exists)
    return {{"error", "engine_failure"}};
  bind_text(exists.get(), 1, table);
  if (sqlite3_step(exists.get()) != SQLITE_ROW)
    return {{"entries", json::array()},
            {"offset", offset},
            {"has_more", false},
            {"normalized", text}};
  exists.reset();
  if (request.value("exact", false))
    where = key + "=?1 AND " + word + "=?5";
  auto stmt =
      prepare(db.get(), ("SELECT " + key + "," + word + ",weight FROM \"" +
                         table + "\" WHERE " + where + " ORDER BY " + order +
                         " LIMIT ?2 OFFSET ?3")
                            .c_str());
  if (!stmt)
    return {{"error", "engine_failure"}};
  bind_text(stmt.get(), 1, text);
  sqlite3_bind_int(stmt.get(), 2, limit + 1);
  sqlite3_bind_int(stmt.get(), 3, offset);
  if (request.value("exact", false))
    bind_text(stmt.get(), 5, request.at("word"));
  if (kind == "english") {
    if (text.empty())
      return {{"error", "invalid_request"}};
    auto upper = text;
    ++upper.back();
    bind_text(stmt.get(), 4, upper);
  }
  auto entries = json::array();
  int code;
  while ((code = sqlite3_step(stmt.get())) == SQLITE_ROW) {
    entries.push_back(
        {{"kind", kind},
         {"code",
          reinterpret_cast<const char *>(sqlite3_column_text(stmt.get(), 0))},
         {"word",
          reinterpret_cast<const char *>(sqlite3_column_text(stmt.get(), 1))},
         {"weight", sqlite3_column_int64(stmt.get(), 2)}});
  }
  if (code != SQLITE_DONE)
    return {{"error", "engine_failure"}};
  bool more = entries.size() > static_cast<std::size_t>(limit);
  if (more)
    entries.erase(entries.end() - 1);
  return {{"entries", entries},
          {"offset", offset},
          {"has_more", more},
          {"normalized", text}};
}

// Which (code, word) pairs the shipped pinyin dictionary already holds. Codes are ' separated syllables exactly as stored; the table name comes from Engine.
inline nlohmann::json listed_pinyin_batch(const nlohmann::json &request,
                                          const std::filesystem::path &path) {
  using namespace backend_ranking;
  const auto &entries = request.at("entries");
  if (!entries.is_array() || entries.empty() || entries.size() > 50)
    return {{"error", "invalid_request"}};
  sqlite3 *raw = nullptr;
  if (sqlite3_open_v2(path.string().c_str(), &raw, SQLITE_OPEN_READONLY,
                      nullptr) != SQLITE_OK) {
    if (raw)
      sqlite3_close(raw);
    return {{"error", "resources_unavailable"}};
  }
  DB db(raw, sqlite3_close);
  auto listed = json::array();
  for (const auto &entry : entries) {
    const auto code = entry.at("code").get<std::string>();
    const auto word = entry.at("word").get<std::string>();
    const auto table = quanpin::build_table_name(quanpin::split_segments(code));
    if (word.empty() || table.empty() || table.find('"') != std::string::npos)
      return {{"error", "invalid_request"}};
    auto exists = prepare(
        db.get(), "SELECT 1 FROM sqlite_master WHERE type='table' AND name=?1");
    if (!exists)
      return {{"error", "engine_failure"}};
    bind_text(exists.get(), 1, table);
    if (sqlite3_step(exists.get()) != SQLITE_ROW) {
      listed.push_back(false);
      continue;
    }
    auto stmt = prepare(db.get(), ("SELECT 1 FROM \"" + table +
                                   "\" WHERE key=?1 AND value=?2 LIMIT 1")
                                      .c_str());
    if (!stmt)
      return {{"error", "engine_failure"}};
    bind_text(stmt.get(), 1, code);
    bind_text(stmt.get(), 2, word);
    const int step = sqlite3_step(stmt.get());
    if (step != SQLITE_ROW && step != SQLITE_DONE)
      return {{"error", "engine_failure"}};
    listed.push_back(step == SQLITE_ROW);
  }
  return {{"listed", listed}};
}
