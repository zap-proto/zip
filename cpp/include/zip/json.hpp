// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: BSD-3-Clause-Eco
//
// JSON, at the size the edge actually needs.
//
// A body arrives as text and a field is a value in it; this reads that text into
// a tree and writes a tree back out. It is not a general JSON library and does
// not want to be — the typed half is generated from the manifest, so what is
// hand-written here is only the part that has no types in it.
//
// A number is kept as text until someone asks for it as a number. That is what
// lets ONE reader serve a body and a URL: a query string carries `?height=7`,
// where 7 is text, and a body carries {"height":7}, where it is a number, and a
// uint32 field reads both without a second binder deciding which it is looking
// at.

#ifndef ZIP_JSON_HPP_
#define ZIP_JSON_HPP_

#include <charconv>
#include <cstdint>
#include <cstdlib>
#include <map>
#include <string>
#include <string_view>
#include <vector>

namespace zip::json {

class Value;
using Object = std::vector<std::pair<std::string, Value>>;
using Array = std::vector<Value>;

enum class Kind { Null, Bool, Number, Text, List, Record };

class Value {
  public:
    Value() = default;
    static Value boolean(bool b) { Value v; v.k_ = Kind::Bool; v.b_ = b; return v; }
    static Value number(std::string n) { Value v; v.k_ = Kind::Number; v.s_ = std::move(n); return v; }
    static Value text(std::string s) { Value v; v.k_ = Kind::Text; v.s_ = std::move(s); return v; }
    // real is a number written the shortest way that reads back as itself. A
    // fixed six-decimal spelling would make 100 print as 100.000000, which is
    // the same value and a different document.
    static Value real(double d) {
        char buf[32];
        const auto [end, err] = std::to_chars(buf, buf + sizeof(buf), d);
        return number(err == std::errc() ? std::string(buf, end) : "0");
    }
    static Value list() { Value v; v.k_ = Kind::List; return v; }
    static Value record() { Value v; v.k_ = Kind::Record; return v; }

    Kind kind() const { return k_; }
    bool null() const { return k_ == Kind::Null; }

    // The scalar readings. Each answers what the value IS if it can and a zero
    // if it cannot, because a field that is absent and a field that is empty
    // reach a handler the same way: as the zero of its type.
    bool as_bool() const {
        if (k_ == Kind::Bool) return b_;
        if (k_ == Kind::Text || k_ == Kind::Number) return s_ == "true" || s_ == "1";
        return false;
    }
    std::int64_t as_int() const { return s_.empty() ? 0 : std::strtoll(s_.c_str(), nullptr, 10); }
    std::uint64_t as_uint() const { return s_.empty() ? 0 : std::strtoull(s_.c_str(), nullptr, 10); }
    double as_double() const { return s_.empty() ? 0 : std::strtod(s_.c_str(), nullptr); }
    const std::string& str() const { return s_; }

    Array& items() { return a_; }
    const Array& items() const { return a_; }
    Object& body() { return o_; }
    const Object& body() const { return o_; }

    // at is the member under name, or a null value. A null value reads as every
    // zero, so an absent member needs no branch at the call site.
    const Value& at(std::string_view name) const {
        for (const auto& [k, v] : o_) {
            if (k == name) return v;
        }
        static const Value none;
        return none;
    }
    bool has(std::string_view name) const {
        for (const auto& [k, v] : o_) {
            if (k == name) return true;
        }
        return false;
    }
    // set adds a member; put replaces the one already there. A record with two
    // members of one name is a record whose reader has to pick, so a table that
    // is FILLED IN — a definition claimed before its fields are walked — puts.
    void set(std::string name, Value v) { o_.emplace_back(std::move(name), std::move(v)); }
    void put(std::string name, Value v) {
        for (auto& [k, old] : o_) {
            if (k == name) { old = std::move(v); return; }
        }
        o_.emplace_back(std::move(name), std::move(v));
    }
    // raw is a value already written as JSON — an example the author wrote,
    // carried through without being re-spelled.
    static Value raw(std::string json) { return number(std::move(json)); }
    void push(Value v) { a_.push_back(std::move(v)); }

    std::string write() const;

  private:
    Kind k_ = Kind::Null;
    bool b_ = false;
    std::string s_;
    Array a_;
    Object o_;
};

// read parses one value out of text. It answers false on anything malformed
// rather than guessing: a body that is not JSON is a request that cannot be
// answered, and a half-read one is worse than none.
bool read(std::string_view in, Value* out);

// escape writes a JSON string body, without the quotes.
std::string escape(std::string_view s);

// ---- implementation ---------------------------------------------------------

inline std::string escape(std::string_view s) {
    std::string out;
    out.reserve(s.size() + 2);
    for (unsigned char c : s) {
        switch (c) {
            case '"': out += "\\\""; break;
            case '\\': out += "\\\\"; break;
            case '\n': out += "\\n"; break;
            case '\r': out += "\\r"; break;
            case '\t': out += "\\t"; break;
            default:
                if (c < 0x20) {
                    static const char* hex = "0123456789abcdef";
                    out += "\\u00";
                    out += hex[c >> 4];
                    out += hex[c & 0xf];
                } else {
                    out += char(c);
                }
        }
    }
    return out;
}

inline std::string Value::write() const {
    switch (k_) {
        case Kind::Null: return "null";
        case Kind::Bool: return b_ ? "true" : "false";
        case Kind::Number: return s_.empty() ? "0" : s_;
        case Kind::Text: return "\"" + escape(s_) + "\"";
        case Kind::List: {
            std::string out = "[";
            for (std::size_t i = 0; i < a_.size(); ++i) {
                if (i) out += ",";
                out += a_[i].write();
            }
            return out + "]";
        }
        case Kind::Record: {
            std::string out = "{";
            bool first = true;
            for (const auto& [k, v] : o_) {
                if (!first) out += ",";
                first = false;
                out += "\"" + escape(k) + "\":" + v.write();
            }
            return out + "}";
        }
    }
    return "null";
}

namespace detail {

struct Reader {
    std::string_view in;
    std::size_t i = 0;

    void space() {
        while (i < in.size() && (in[i] == ' ' || in[i] == '\t' || in[i] == '\n' || in[i] == '\r')) ++i;
    }
    bool literal(std::string_view want) {
        if (in.compare(i, want.size(), want) != 0) return false;
        i += want.size();
        return true;
    }
    bool string(std::string* out) {
        if (i >= in.size() || in[i] != '"') return false;
        ++i;
        while (i < in.size()) {
            char c = in[i++];
            if (c == '"') return true;
            if (c != '\\') { *out += c; continue; }
            if (i >= in.size()) return false;
            char e = in[i++];
            switch (e) {
                case '"': *out += '"'; break;
                case '\\': *out += '\\'; break;
                case '/': *out += '/'; break;
                case 'b': *out += '\b'; break;
                case 'f': *out += '\f'; break;
                case 'n': *out += '\n'; break;
                case 'r': *out += '\r'; break;
                case 't': *out += '\t'; break;
                case 'u': {
                    if (i + 4 > in.size()) return false;
                    unsigned code = 0;
                    for (int k = 0; k < 4; ++k) {
                        char h = in[i++];
                        code <<= 4;
                        if (h >= '0' && h <= '9') code |= unsigned(h - '0');
                        else if (h >= 'a' && h <= 'f') code |= unsigned(h - 'a' + 10);
                        else if (h >= 'A' && h <= 'F') code |= unsigned(h - 'A' + 10);
                        else return false;
                    }
                    // One code point, written as UTF-8. A surrogate half is
                    // written as it stands: this reads bodies, it does not
                    // repair them.
                    if (code < 0x80) {
                        *out += char(code);
                    } else if (code < 0x800) {
                        *out += char(0xC0 | (code >> 6));
                        *out += char(0x80 | (code & 0x3F));
                    } else {
                        *out += char(0xE0 | (code >> 12));
                        *out += char(0x80 | ((code >> 6) & 0x3F));
                        *out += char(0x80 | (code & 0x3F));
                    }
                    break;
                }
                default: return false;
            }
        }
        return false;
    }
    bool value(Value* out) {
        space();
        if (i >= in.size()) return false;
        switch (in[i]) {
            case 'n':
                if (!literal("null")) return false;
                *out = Value();
                return true;
            case 't':
                if (!literal("true")) return false;
                *out = Value::boolean(true);
                return true;
            case 'f':
                if (!literal("false")) return false;
                *out = Value::boolean(false);
                return true;
            case '"': {
                std::string s;
                if (!string(&s)) return false;
                *out = Value::text(std::move(s));
                return true;
            }
            case '[': {
                ++i;
                *out = Value::list();
                space();
                if (i < in.size() && in[i] == ']') { ++i; return true; }
                for (;;) {
                    Value v;
                    if (!value(&v)) return false;
                    out->push(std::move(v));
                    space();
                    if (i < in.size() && in[i] == ',') { ++i; continue; }
                    if (i < in.size() && in[i] == ']') { ++i; return true; }
                    return false;
                }
            }
            case '{': {
                ++i;
                *out = Value::record();
                space();
                if (i < in.size() && in[i] == '}') { ++i; return true; }
                for (;;) {
                    space();
                    std::string k;
                    if (!string(&k)) return false;
                    space();
                    if (i >= in.size() || in[i] != ':') return false;
                    ++i;
                    Value v;
                    if (!value(&v)) return false;
                    out->set(std::move(k), std::move(v));
                    space();
                    if (i < in.size() && in[i] == ',') { ++i; continue; }
                    if (i < in.size() && in[i] == '}') { ++i; return true; }
                    return false;
                }
            }
            default: {
                const std::size_t start = i;
                if (in[i] == '-' || in[i] == '+') ++i;
                while (i < in.size() && ((in[i] >= '0' && in[i] <= '9') || in[i] == '.' ||
                                         in[i] == 'e' || in[i] == 'E' || in[i] == '-' || in[i] == '+')) {
                    ++i;
                }
                if (i == start) return false;
                *out = Value::number(std::string(in.substr(start, i - start)));
                return true;
            }
        }
    }
};

}  // namespace detail

inline bool read(std::string_view in, Value* out) {
    detail::Reader r{in};
    if (!r.value(out)) return false;
    r.space();
    return r.i == in.size();
}

}  // namespace zip::json

#endif  // ZIP_JSON_HPP_
