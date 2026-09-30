#include <nlohmann/json.hpp>
#include <algorithm>
#include "catalog.h"
#include "ranking.h"
#include "journal_snapshot.h"
#include "user_dictionary/user_dictionary_journal.h"
#include <fstream>
#include <SimpleConverter.hpp>
#include <cpp-pinyin/Pinyin.h>
#include <cpp-pinyin/G2pglobal.h>
#include "contracts/assets/assets.h"
#include "local_modes/unicode_query.h"
#include "local_modes/date_time_query.h"
#include "local_modes/emoji_query.h"
#include "local_modes/kaomoji_query.h"
#include "local_modes/jianpin_query.h"
#include "english/english_dictionary.h"
#include "include/metasequoia/personal_dictionary.h"
#include "common/helpcode_utils.h"
#include "quanpin/quanpin_query.h"
#include "schemes/quanpin_scheme.h"
#include "schemes/shuangpin_scheme.h"
#include "schemes/wubi_scheme.h"
#include "providers/pinyin_candidate_provider.h"
#include "providers/wubi_candidate_provider.h"
#include "local_modes/quick_phrase_query.h"
#include "dictionary_catalog.h"
#include "japanese/romaji_converter.h"
#include "schemes/japanese_romaji_scheme.h"
#include "providers/japanese_candidate_provider.h"
#include <filesystem>
#include <iostream>
#include <stdexcept>

using json = nlohmann::json;
using namespace metasequoia;

static json candidates(const std::vector<WordItem>& items) {
    auto out = json::array();
    for (const auto& item : items) {
        out.push_back({{"code", item.pinyin}, {"canonical_pinyin", item.canonical_pinyin},
                       {"word", item.word}, {"weight", item.weight}, {"fixed_position", item.fixed_position}});
    }
    return {{"candidates", out}};
}

// One bounded request per process: mutable Engine globals never cross users or requests.
// Paths come only from the host process argument, never from the JSON request.
static json execute(const json& request, const std::filesystem::path& resources, const std::filesystem::path& scratch, const std::filesystem::path& dictionaries = {}) {
    const auto dictionary_root = dictionaries.empty() ? resources : dictionaries;
    const auto op = request.at("operation").get<std::string>();
    const auto text = request.value("text", std::string());
    const int limit = request.value("limit", 20);
    if (limit < 1 || limit > 200 || text.size() > 8192) throw std::invalid_argument("invalid_request");
    if(op=="romaji") {
        const auto direction=request.value("direction",std::string("romaji-hiragana"));
        if(direction=="romaji-hiragana") {const auto result=japanese::ConvertRomaji(text);return {{"text",result.hiragana},{"pending",result.pending},{"complete",result.complete}};}
        if(direction=="hiragana-katakana")return {{"text",japanese::HiraganaToKatakana(text)}};
        if(direction=="kana-romaji")return {{"text",japanese::HiraganaToRomaji(text)}};
        return {{"error","invalid_request"}};
    }
    if(op=="japanese") {
        const auto dictionary=resources/assets::main_dictionary,model=resources/assets::japanese_model;
        if(!resources.is_absolute()||!std::filesystem::is_regular_file(dictionary)||!std::filesystem::is_regular_file(model))return {{"error","resources_unavailable"}};
        {japanese::JapaneseSentenceDecoder check(model.string());if(!check.ready())return {{"error","resources_unavailable"}};}
        JapaneseRomajiScheme input;input.set_raw_input(text,text);const auto query=input.build_request();
        if(!query.valid)return {{"error","invalid_request"}};
        JapaneseCandidateProvider provider(dictionary.string(),model.string());auto items=provider.query(query);
        if(items.size()>static_cast<std::size_t>(limit))items.resize(limit);
        const auto conversion=japanese::ConvertRomaji(text);auto result=candidates(items);
        result["hiragana"]=conversion.hiragana;result["pending"]=conversion.pending;result["complete"]=conversion.complete;return result;
    }
    if (op == "validate_snapshot") {
        std::ifstream input(scratch / "snapshot.jsonl");
        if (!input) return {{"error","engine_failure"}};
        std::string line;
        while(std::getline(input,line)) {
            if(line.size()>65536)return {{"error","invalid_request"}};
            const auto record=json::parse(line);
            const auto type=record.at("type").get<std::string>();
            if(type!="entry" && type!="overlay")continue;
            const auto& entry=record.at("data");
            auto weight=entry.at("weight").get<std::int64_t>();
            if(type=="overlay" && record.value("deleted",false))weight=10;
            auto validated=execute({{"operation","validate_dictionary"},{"kind",entry.at("kind")},{"code",entry.at("code")},{"text",entry.at("word")},{"weight",weight}},resources,scratch);
            if(validated.contains("error") || validated.at("code")!=entry.at("code") || validated.at("word")!=entry.at("word"))return {{"error","invalid_request"}};
        }
        if(input.bad())return {{"error","engine_failure"}};
        return {{"valid",true}};
    }
    if (op == "personal_query" || op == "personal_rank" || op == "personal_delete") {
        if (scratch.empty() || !scratch.is_absolute() || !resources.is_absolute()) return {{"error","resources_unavailable"}};
        const auto user = scratch / "user";
        std::filesystem::create_directories(user);
        const auto journal = (user / assets::user_journal).string();
        if (!user_dictionary::ensure_user_database(journal)) return {{"error","engine_failure"}};
        std::ifstream input(scratch / "snapshot.jsonl");
        if (!input) return {{"error","engine_failure"}};
        std::string line;
        std::int64_t revision=0;
        bool has_overlay=false;
        backend_ranking::SnapshotWriter snapshot_writer(journal);
        if(!snapshot_writer.ready())return {{"error","engine_failure"}};
        while (std::getline(input,line)) {
            if (line.size()>65536) return {{"error","engine_failure"}};
            const auto change = json::parse(line);
            if (change.contains("snapshot_revision")) {revision=change.at("snapshot_revision").get<std::int64_t>();continue;}
            if(change.contains("selection")) {if(!snapshot_writer.selection(change.at("selection")))return {{"error","engine_failure"}};continue;}
            if(change.contains("fixed")) {if(!snapshot_writer.position(change.at("fixed")))return {{"error","engine_failure"}};continue;}
            has_overlay=true;
            if(!snapshot_writer.entry(change.at("previous"),true)||!snapshot_writer.entry(change.at("replacement"),false))return {{"error","engine_failure"}};
        }
        if(!snapshot_writer.commit())return {{"error","engine_failure"}};
        if (input.bad()) return {{"error","engine_failure"}};
        auto nested=request.at("query");
        const auto operation=nested.at("operation").get<std::string>();
        if(operation!="candidates"&&operation!="english"&&operation!="quick"&&operation!="jianpin"&&operation!="dictionary")throw std::invalid_argument("invalid_request");
        const auto projected=(has_overlay || op=="personal_rank" || op=="personal_delete") ? prepare_runtime_paths(resources,user,scratch/"cache","backend").dictionaries : resources;
        const int requested_limit=nested.value("limit",20);
        if(operation!="dictionary")nested["limit"]=200;
        auto response = execute(nested,resources,scratch,projected);
        if(response.contains("error"))return response;
        if(operation=="dictionary"){response["revision"]=revision;return response;}
        const auto query_text=nested.at("text").get<std::string>();
        const auto scheme_name=nested.value("scheme",std::string("pinyin"));
        const auto scheme=scheme_name=="wubi"?SchemeType::Wubi:scheme_name=="shuangpin"?SchemeType::Shuangpin:SchemeType::Quanpin;
        const auto& profile=GetShuangpinProfile(nested.value("profile",std::string("xiaohe")));
        std::string context;
        std::string ranking_context;
        if(operation=="english")context="english:"+query_text;
        else if(operation=="jianpin")context=local_modes::jianpin_ranking_context(query_text,scheme,profile);
        else if(operation=="candidates") {
            context=response.value("normalized_segmentation",std::string());
            ranking_context=scheme==SchemeType::Wubi?query_text:context;
            if(scheme!=SchemeType::Wubi && query_text.size()!=1) {
                auto plain=context;plain.erase(std::remove(plain.begin(),plain.end(),'\''),plain.end());
                const auto cuts=quanpin::cut_pinyin_by_mode(plain,"correction");
                if(!cuts.empty())context=quanpin::join_segments(cuts.front());
            }
        }
        std::vector<WordItem> items;
        for(const auto& item:response.at("candidates"))items.emplace_back(item.at("code"),item.at("word"),item.at("weight"),operation=="english"?CandidateSource::EnglishDictionary:CandidateSource::Database,item.value("canonical_pinyin",std::string()));
        if(op=="personal_rank" || op=="personal_delete") {
            if(operation=="quick")return {{"error","invalid_request"}};
            const auto kind=operation=="english"?"english":scheme==SchemeType::Wubi?"wubi":"pinyin";
            auto result=backend_ranking::apply(request.at("action"),op=="personal_delete",operation=="candidates"?ranking_context:context,items,kind,(projected/(operation=="english"?assets::english_dictionary:assets::main_dictionary)).string(),journal);
            result["revision"]=revision;
            return result;
        }
        const bool include_missing=operation=="candidates" && scheme!=SchemeType::Wubi && query_text.size()==1;
        if(include_missing) {
            PinyinCandidateProvider provider(profile,RuntimePaths{resources,scratch,scratch,projected});
            user_dictionary::apply_fixed_positions(journal,context,items,true,[&](const std::string& key,const std::string& word){return provider.find_candidate(scheme,key,word);});
        } else user_dictionary::apply_fixed_positions(journal,context,items,false,{},operation=="english");
        if(items.size()>static_cast<std::size_t>(requested_limit))items.resize(requested_limit);
        response["candidates"]=candidates(items).at("candidates");
        response["context"]=context;
        response["revision"]=revision;
        return response;
    }
    if (op == "annotate_batch") {
        const auto& words = request.at("words");
        if (!words.is_array() || words.empty() || words.size() > 50) throw std::invalid_argument("invalid_request");
        auto entries = json::array();
        for (const auto& word : words) {
            auto result = execute({{"operation", "annotate"}, {"text", word}}, resources, scratch);
            if (result.contains("error")) return result;
            entries.push_back(result);
        }
        return {{"entries", entries}};
    }
    if (op == "validate_dictionary_batch") {
        const auto& entries = request.at("entries");
        if (!entries.is_array() || entries.empty() || entries.size() > 50) throw std::invalid_argument("invalid_request");
        auto validated = json::array();
        for (auto entry : entries) {
            entry["operation"] = "validate_dictionary";
            const auto result = execute(entry, resources, scratch);
            if (result.contains("error")) return result;
            validated.push_back(result);
        }
        return {{"entries", validated}};
    }
    if (op == "unicode") return candidates(local_modes::query_unicode(text, limit));
    if (op == "datetime") {
        const auto& date = request.at("date");
        local_modes::LocalDateTime now{date.at("year"), date.at("month"), date.at("day"),
                                       date.at("weekday"), date.at("hour"), date.at("minute"), date.at("second")};
        if (now.year < 1 || now.year > 9999 || now.month < 1 || now.month > 12 || now.day < 1 || now.day > 31 ||
            now.weekday > 6 || now.hour > 23 || now.minute > 59 || now.second > 59 ||
            !local_modes::is_date_time_keyword(text)) throw std::invalid_argument("invalid_request");
        return candidates(local_modes::query_date_time(text, &now, limit));
    }
    if(op=="dictionary")return query_dictionary_catalog(request,dictionary_root);
    if (op == "listed_pinyin_batch") return listed_pinyin_batch(request, dictionary_root / assets::main_dictionary);
    if (op == "listed_english_batch") return listed_english_batch(request, dictionary_root / assets::english_dictionary);
    if (op == "pinyin_weight_medians") return pinyin_weight_medians(dictionary_root / assets::main_dictionary);
    if (op == "validate_dictionary") {
        const auto kind = request.at("kind").get<std::string>();
        PersonalDictionaryKind type;
        if (kind == "pinyin") type = PersonalDictionaryKind::Pinyin;
        else if (kind == "wubi") type = PersonalDictionaryKind::Wubi;
        else if (kind == "quick") type = PersonalDictionaryKind::QuickPhrase;
        else if (kind == "english") type = PersonalDictionaryKind::English;
        else throw std::invalid_argument("invalid_request");
        auto result = validate_personal_dictionary_entry({type, request.at("code"), text, request.value("weight", 100000LL)});
        if (!result.entry) return {{"error", "invalid_dictionary_entry"}};
        return {{"kind", kind}, {"code", result.entry->key}, {"word", result.entry->value}, {"weight", result.entry->weight}};
    }
    const auto scheme_name = request.value("scheme", std::string("pinyin"));
    SchemeType scheme;
    if (scheme_name == "pinyin") scheme = SchemeType::Quanpin;
    else if (scheme_name == "shuangpin") scheme = SchemeType::Shuangpin;
    else if (scheme_name == "wubi") scheme = SchemeType::Wubi;
    else throw std::invalid_argument("invalid_request");
    const auto& profile = GetShuangpinProfile(request.value("profile", std::string("xiaohe")));
    QueryRequest query;
    if (op == "candidates" || op == "cloud_candidates" || op == "segmentation") {
        if (scheme == SchemeType::Quanpin) {
            QuanpinScheme input; input.set_raw_input(text, text); query = input.build_request();
        } else if (scheme == SchemeType::Shuangpin) {
            ShuangpinScheme input(profile); input.set_raw_input(text, text); query = input.build_request();
        } else {
            WubiScheme input; input.set_raw_input(text, text); query = input.build_request();
        }
        if (!query.valid) throw std::invalid_argument("invalid_request");
        if (op == "segmentation") return {{"raw", query.raw_segmentation}, {"normalized", query.normalized_segmentation}};
    }
    if (resources.empty() || !resources.is_absolute()) return {{"error", "resources_unavailable"}};
    if (op == "annotate") {
        const auto dictionary = resources / "pinyin";
        if (!std::filesystem::is_directory(dictionary)) return {{"error", "resources_unavailable"}};
        static std::unique_ptr<Pinyin::Pinyin> annotator;
        if (!annotator) {Pinyin::setDictionaryPath(dictionary); annotator = std::make_unique<Pinyin::Pinyin>();}
        if (!annotator->initialized()) return {{"error", "resources_unavailable"}};
        const auto result = annotator->hanziToPinyin(text, Pinyin::ManTone::Style::NORMAL, Pinyin::Error::Default, false, false, false);
        std::string code;
        for (const auto& item : result) {
            if (item.error || item.pinyin.empty()) return {{"error", "invalid_request"}};
            auto syllable = item.pinyin;
            for (std::size_t pos = 0; (pos = syllable.find("ü", pos)) != std::string::npos;) syllable.replace(pos, 2, "v");
            if (!code.empty()) code += "'";
            code += syllable;
        }
        const auto validated = validate_personal_dictionary_entry({PersonalDictionaryKind::Pinyin, code, text, 10});
        if (!validated.entry) return {{"error", "invalid_dictionary_entry"}};
        return {{"code", validated.entry->key}, {"word", text}};
    }
    if (op == "convert") {
        const auto config = resources / "opencc" / "s2t.json";
        if (!std::filesystem::is_regular_file(config)) return {{"error", "resources_unavailable"}};
        opencc::SimpleConverter converter(config.string());
        return {{"text", converter.Convert(text)}, {"conversion", "s2t"}};
    }
    if (op == "catalog") return query_catalog(request, resources);
    if (op == "helpcode") {
        const auto schema = request.value("schema", std::string("lantian"));
        if (!HelpcodeUtils::is_supported_helpcode_schema(schema)) throw std::invalid_argument("invalid_request");
        const auto keymap = HelpcodeUtils::load_helpcode_keymap(resources, schema);
        if (!keymap || keymap->empty()) return {{"error", "resources_unavailable"}};
        return {{"text", HelpcodeUtils::compute_helpcodes(text, false, keymap.get())}, {"schema", schema}};
    }
    if (op == "emoji" || op == "kaomoji" || op == "jianpin" || op == "quick") {
        auto path = (op == "jianpin" || op == "quick") ? dictionary_root/assets::main_dictionary : resources/assets::other_dictionary;
        if (!std::filesystem::is_regular_file(path)) return {{"error", "resources_unavailable"}};
        local_modes::LocalQueryResult result;
        if (op == "emoji") result = local_modes::query_emoji(text, scheme, path, limit, profile);
        else if (op == "kaomoji") result = local_modes::query_kaomoji(text, scheme, path, limit, profile);
        else if (op == "jianpin") result = local_modes::query_jianpin(text, scheme, path, limit, profile);
        else result = local_modes::query_quick_phrases(text, path, limit);
        if (result.diagnostic) return {{"error", "resources_unavailable"}};
        return candidates(result.candidates);
    }
    if (op == "candidates" || op == "cloud_candidates") {
        auto path = dictionary_root / assets::main_dictionary;
        if (!std::filesystem::is_regular_file(path) || scratch.empty() || !scratch.is_absolute())
            return {{"error", "resources_unavailable"}};
        std::vector<WordItem> items;
        if (scheme == SchemeType::Wubi) {
            WubiCandidateProvider provider(path.string()); items = provider.query(query);
        } else {
            RuntimePaths paths{resources, scratch, scratch, dictionary_root};
            PinyinCandidateProvider provider(profile, paths); items = provider.query(query);
        }
        if (op == "cloud_candidates") {
            // The cloud contract has no replacement span: only whole-input
            // dictionary entries are safe, never prefixes or generated phrases.
            items.erase(std::remove_if(items.begin(), items.end(), [&](const WordItem& item) {
                const auto& key = item.canonical_pinyin.empty() ? item.pinyin : item.canonical_pinyin;
                return item.source != CandidateSource::Database || key != query.normalized_segmentation;
            }), items.end());
        }
        if (items.size() > static_cast<std::size_t>(limit)) items.resize(limit);
        auto result = candidates(items);
        result["raw_segmentation"] = query.raw_segmentation;
        result["normalized_segmentation"] = query.normalized_segmentation;
        return result;
    }
    if (op == "english" || op == "gloss") {
        auto path = dictionary_root / "english.db";
        if (!std::filesystem::is_regular_file(path)) return {{"error", "resources_unavailable"}};
        EnglishDictionary dictionary(path.string(), false);
        if (!dictionary.ready()) return {{"error", "resources_unavailable"}};
        if (op == "english") return candidates(dictionary.query_prefix(text, limit));
        const auto direction = request.value("direction", std::string("en-zh"));
        if (direction != "en-zh" && direction != "zh-en") throw std::invalid_argument("invalid_request");
        return {{"text", direction == "en-zh" ? dictionary.query_chinese_gloss(text) : dictionary.query_english_gloss(text)}};
    }
    return {{"error", "unknown_operation"}};
}

int main(int argc, char** argv) {
    try {
        std::string raw;
        char c;
        while (std::cin.get(c)) {
            if (raw.size() >= 65536) throw std::invalid_argument("invalid_request");
            raw.push_back(c);
        }
        auto response = execute(json::parse(raw), argc >= 2 ? std::filesystem::u8path(argv[1]) : std::filesystem::path(),
                                argc == 3 ? std::filesystem::u8path(argv[2]) : std::filesystem::path());
        std::cout << response.dump() << '\n';
    } catch (const json::exception&) {
        std::cout << "{\"error\":\"invalid_request\"}\n";
    } catch (const std::invalid_argument&) {
        std::cout << "{\"error\":\"invalid_request\"}\n";
    } catch (...) {
        // Native diagnostics may contain paths/input: never forward them to HTTP clients or logs.
        std::cout << "{\"error\":\"engine_failure\"}\n";
    }
}
