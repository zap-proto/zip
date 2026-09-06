// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: BSD-3-Clause-Eco
//
// The C++ front end: ops, read off C++.
//
// A Go program is its own extractor — it reflects on itself at run time — and a
// Rust macro runs inside the compiler. C++ has neither: there is no run-time
// type graph and, until reflection lands, no in-language mechanism at all. So
// the extractor is a pass over the source with the compiler's own parser, which
// is the only reader that agrees with the compiler about what the source says.
//
// It reads what the developer already wrote and nothing else:
//
//   zip::get(app, "/chain/bootstrapped", &Info::bootstrapped, &info);
//
// the path from the literal, the input and answer types from the handler's own
// signature, the prose from its /// comment, and a field's wire name from the
// declaration — or from ZIP_JSON where the wire name differs from the field's.
// The result is a manifest, which is what every projection reduces over.
//
// A NON-CONSTANT PATH IS AN ERROR. An op whose address is computed has no
// identity to document: no operationId, no tool name, no command, no route in
// the document. Refusing is the whole point — the alternative is a published
// contract with a hole in it exactly where the interesting route was.

#include <clang-c/CXCompilationDatabase.h>
#include <clang-c/Index.h>

#include <algorithm>
#include <cstdio>
#include <cstring>
#include <map>
#include <set>
#include <string>
#include <vector>

#include "zip/json.hpp"

using zip::json::Value;

namespace {

std::string str(CXString s) {
    const char* c = clang_getCString(s);
    std::string out = c ? c : "";
    clang_disposeString(s);
    return out;
}

[[noreturn]] void fail(const std::string& what) {
    std::fprintf(stderr, "zipc: %s\n", what.c_str());
    std::exit(1);
}

// ---- prose ------------------------------------------------------------------

// text strips a doc comment down to what its author wrote: the markers go, one
// space after them goes, and the line breaks stay — a paragraph is a fact about
// the comment, and the summary rule reads the first sentence out of it later.
std::string text(const std::string& raw) {
    std::vector<std::string> lines;
    std::size_t i = 0;
    while (i <= raw.size()) {
        const std::size_t nl = raw.find('\n', i);
        std::string line = raw.substr(i, nl == std::string::npos ? nl : nl - i);
        while (!line.empty() && (line.front() == ' ' || line.front() == '\t')) line.erase(0, 1);
        if (line.rfind("///", 0) == 0) line.erase(0, 3);
        else if (line.rfind("//!", 0) == 0) line.erase(0, 3);
        else if (line.rfind("//", 0) == 0) line.erase(0, 2);
        else if (line.rfind("/**", 0) == 0) line.erase(0, 3);
        else if (line.rfind("/*", 0) == 0) line.erase(0, 2);
        if (line.rfind("*/", line.size() >= 2 ? line.size() - 2 : 0) == line.size() - 2 && line.size() >= 2) {
            line.erase(line.size() - 2);
        }
        if (line.rfind("*", 0) == 0 && line.size() > 0) line.erase(0, 1);
        if (!line.empty() && line.front() == ' ') line.erase(0, 1);
        while (!line.empty() && (line.back() == ' ' || line.back() == '\t' || line.back() == '\r')) line.pop_back();
        lines.push_back(line);
        if (nl == std::string::npos) break;
        i = nl + 1;
    }
    while (!lines.empty() && lines.back().empty()) lines.pop_back();
    std::size_t first = 0;
    while (first < lines.size() && lines[first].empty()) ++first;
    std::string out;
    for (std::size_t k = first; k < lines.size(); ++k) {
        if (k > first) out += "\n";
        out += lines[k];
    }
    return out;
}

// marker recognises an "Example:" or "Response:" line and returns what follows,
// which is the same grammar the Go pass reads: the body is part of the comment
// because there is one place for what an op says about itself.
bool marker(const std::string& line, std::string* key, std::string* rest) {
    std::string t = line;
    while (!t.empty() && (t.front() == ' ' || t.front() == '\t')) t.erase(0, 1);
    for (const char* k : {"Example", "Response"}) {
        const std::string want = std::string(k) + ":";
        if (t.rfind(want, 0) == 0) {
            *key = k;
            *rest = t.substr(want.size());
            while (!rest->empty() && rest->front() == ' ') rest->erase(0, 1);
            return true;
        }
    }
    return false;
}

struct Doc {
    std::string description;
    std::string example;   // compacted JSON, empty when the comment gave none
    std::string response;
};

// split separates the prose from the Example:/Response: bodies. A body may run
// onto the following lines — it is read until it parses — and is compacted, so
// the manifest is the same whatever the source formatting.
Doc split(const std::string& body) {
    Doc d;
    std::vector<std::string> lines;
    std::size_t i = 0;
    while (i <= body.size()) {
        const std::size_t nl = body.find('\n', i);
        lines.push_back(body.substr(i, nl == std::string::npos ? nl : nl - i));
        if (nl == std::string::npos) break;
        i = nl + 1;
    }
    std::vector<std::string> keep;
    for (std::size_t k = 0; k < lines.size(); ++k) {
        std::string key, rest;
        if (!marker(lines[k], &key, &rest)) {
            keep.push_back(lines[k]);
            continue;
        }
        std::string json = rest;
        Value v;
        for (std::size_t j = k + 1; j < lines.size() && !zip::json::read(json, &v); ++j) {
            std::string k2, r2;
            if (marker(lines[j], &k2, &r2)) break;
            json += "\n" + lines[j];
            k = j;
        }
        if (!zip::json::read(json, &v)) fail(key + ": invalid JSON in a doc comment: " + json);
        (key == "Example" ? d.example : d.response) = v.write();
    }
    while (!keep.empty() && keep.back().empty()) keep.pop_back();
    for (std::size_t k = 0; k < keep.size(); ++k) {
        if (k) d.description += "\n";
        d.description += keep[k];
    }
    return d;
}

// ---- annotations ------------------------------------------------------------

// notes are the ZIP_ macros a declaration carries. A standard [[attribute]] comes
// back from libclang with no recoverable text, so the annotation is
// __attribute__((annotate("..."))) behind a macro — which the compiler discards,
// so a wire name costs nothing at run time.
std::vector<std::string> notes(CXCursor c) {
    std::vector<std::string> out;
    clang_visitChildren(
        c,
        [](CXCursor kid, CXCursor, CXClientData d) {
            if (clang_getCursorKind(kid) == CXCursor_AnnotateAttr) {
                static_cast<std::vector<std::string>*>(d)->push_back(str(clang_getCursorSpelling(kid)));
            }
            return CXChildVisit_Continue;
        },
        &out);
    return out;
}

bool note(const std::vector<std::string>& all, const std::string& key, std::string* value) {
    for (const std::string& n : all) {
        if (n == key) {
            *value = "";
            return true;
        }
        if (n.rfind(key + ":", 0) == 0) {
            *value = n.substr(key.size() + 1);
            return true;
        }
    }
    return false;
}

// ---- the walk ---------------------------------------------------------------

struct Extract {
    Value structs = Value::record();
    std::vector<Value> ops;
    std::map<std::string, std::string> refs;  // the record's spelling → its key
    // What each declaration says about its own members, and which other
    // declarations it reaches. An op's prose is every word written on every type
    // it can see, which is what the document, the tool schema and the CLI help
    // all read — one comment, three surfaces.
    std::map<std::string, std::map<std::string, std::string>> prose;
    std::map<std::string, std::vector<std::string>> reaches;
    int anon = 0;

    // home is the namespace a declaration was made in, which is what the
    // manifest calls its package: it qualifies prose and schema names, and two
    // services in one process answering the same address are told apart by it.
    static std::string home(CXCursor c) {
        std::vector<std::string> parts;
        for (CXCursor p = clang_getCursorSemanticParent(c); !clang_Cursor_isNull(p);
             p = clang_getCursorSemanticParent(p)) {
            const CXCursorKind k = clang_getCursorKind(p);
            if (k == CXCursor_Namespace) parts.insert(parts.begin(), str(clang_getCursorSpelling(p)));
            if (k == CXCursor_TranslationUnit) break;
        }
        std::string out;
        for (std::size_t i = 0; i < parts.size(); ++i) {
            if (i) out += "::";
            out += parts[i];
        }
        return out;
    }

    static std::string comment(CXCursor c) {
        const std::string raw = str(clang_Cursor_getRawCommentText(c));
        return raw.empty() ? "" : text(raw);
    }

    // A standard-library shape by name: what it IS to the wire, not what it is
    // made of. std::string is text, std::vector is a list, std::optional is the
    // value it may hold.
    static std::string shape(CXType t) {
        CXCursor decl = clang_getTypeDeclaration(t);
        std::string name = str(clang_getCursorSpelling(decl));
        if (name.rfind("basic_string", 0) == 0) return "text";
        if (name == "vector" || name == "list" || name == "deque" || name == "set") return "list";
        if (name == "map" || name == "unordered_map") return "map";
        if (name == "array") return "array";
        if (name == "optional") return "optional";
        if (name == "string_view" || name == "basic_string_view") return "text";
        return "";
    }

    static CXType arg(CXType t, unsigned i) {
        return clang_Type_getTemplateArgumentAsType(t, int(i));
    }

    // typ is what a value IS. It follows a reference, a pointer and a typedef to
    // the thing itself, because the wire carries a value and never the way this
    // language happened to hold it.
    Value typ(CXType t) {
        t = clang_getCanonicalType(clang_getNonReferenceType(t));
        while (t.kind == CXType_Pointer) t = clang_getCanonicalType(clang_getPointeeType(t));
        t = clang_getUnqualifiedType(t);

        Value v = Value::record();
        switch (t.kind) {
            case CXType_Bool:
                v.set("kind", Value::text("bool"));
                return v;
            case CXType_Char_S:
            case CXType_SChar:
            case CXType_Short:
            case CXType_Int:
            case CXType_Long:
            case CXType_LongLong:
                v.set("kind", Value::text("int"));
                v.set("format", Value::text("int" + std::to_string(clang_Type_getSizeOf(t) * 8)));
                return v;
            case CXType_Char_U:
            case CXType_UChar:
            case CXType_UShort:
            case CXType_UInt:
            case CXType_ULong:
            case CXType_ULongLong:
                v.set("kind", Value::text("uint"));
                v.set("format", Value::text("uint" + std::to_string(clang_Type_getSizeOf(t) * 8)));
                return v;
            case CXType_Float:
                v.set("kind", Value::text("float"));
                v.set("format", Value::text("float"));
                return v;
            case CXType_Double:
            case CXType_LongDouble:
                v.set("kind", Value::text("float"));
                v.set("format", Value::text("double"));
                return v;
            case CXType_ConstantArray: {
                v.set("kind", Value::text("array"));
                v.set("elem", typ(clang_getArrayElementType(t)));
                v.set("len", Value::number(std::to_string(clang_getArraySize(t))));
                return v;
            }
            default:
                break;
        }
        if (t.kind == CXType_Record || t.kind == CXType_Elaborated || t.kind == CXType_Unexposed) {
            const std::string s = shape(t);
            if (s == "text") {
                v.set("kind", Value::text("string"));
                return v;
            }
            if (s == "list") {
                v.set("kind", Value::text("slice"));
                v.set("elem", typ(arg(t, 0)));
                return v;
            }
            if (s == "array") {
                v.set("kind", Value::text("array"));
                v.set("elem", typ(arg(t, 0)));
                v.set("len", Value::number("0"));
                return v;
            }
            if (s == "map") {
                v.set("kind", Value::text("map"));
                v.set("key", typ(arg(t, 0)));
                v.set("elem", typ(arg(t, 1)));
                return v;
            }
            if (s == "optional") {
                // An optional is how C++ says a value may be absent, which is a
                // fact about the wire; what it holds is the value itself.
                Value inner = typ(arg(t, 0));
                inner.put("maybe", Value::boolean(true));
                return inner;
            }
            return record(t);
        }
        v.set("kind", Value::text("any"));
        return v;
    }

    // record files a struct's fields under a key and answers with a reference to
    // it. The key is claimed BEFORE the fields are walked — that claim is what a
    // type containing itself finds instead of recursing.
    Value record(CXType t) {
        CXCursor decl = clang_getTypeDeclaration(t);
        const std::string name = str(clang_getCursorSpelling(decl));
        const std::string pkg = home(decl);
        const std::string full = str(clang_getTypeSpelling(t));

        Value v = Value::record();
        v.set("kind", Value::text("struct"));
        v.set("name", Value::text(name));
        if (!pkg.empty()) v.set("pkg", Value::text(pkg));

        if (auto it = refs.find(full); it != refs.end()) {
            v.set("ref", Value::text(it->second));
            return v;
        }
        std::string key = pkg.empty() ? name : pkg + "." + name;
        if (name.empty()) key = "anon" + std::to_string(++anon);
        while (structs.has(key)) key += "'";
        refs[full] = key;
        v.set("ref", Value::text(key));

        Value body = Value::list();
        Value own = Value::list();
        structs.put(key, Value::record());  // claimed, so the walk below terminates

        struct Walk {
            Extract* self;
            Value* body;
            Value* own;
            std::string key;
            std::string name;
        } w{this, &body, &own, key, name};

        clang_visitChildren(
            decl,
            [](CXCursor kid, CXCursor, CXClientData d) {
                Walk* w = static_cast<Walk*>(d);
                const CXCursorKind k = clang_getCursorKind(kid);
                if (k == CXCursor_CXXBaseSpecifier) {
                    // A base is a declaration this one absorbs: its members are
                    // in the object, and a layout gives it one slot.
                    Value t = w->self->typ(clang_getCursorType(kid));
                    for (const Value& f : w->self->members(t)) w->body->push(f);
                    Value f = Value::record();
                    f.set("name", Value::text(str(clang_getCursorSpelling(kid))));
                    f.set("embed", Value::boolean(true));
                    f.set("type", t);
                    w->own->push(std::move(f));
                    return CXChildVisit_Continue;
                }
                if (k != CXCursor_FieldDecl) return CXChildVisit_Continue;
                w->self->field(kid, w->key, w->name, w->body, w->own);
                return CXChildVisit_Continue;
            },
            &w);

        Value def = Value::record();
        if (!name.empty()) def.set("name", Value::text(name));
        if (!pkg.empty()) def.set("pkg", Value::text(pkg));
        def.set("body", body);
        def.set("own", own);
        structs.put(key, std::move(def));
        return v;
    }

    // note_reach remembers the declarations one type leads to, through a list, an
    // array or a map value — the same descent the schema builder makes, so a
    // nested type's prose lands too.
    void note_reach(const std::string& from, const Value& t) {
        if (t.at("kind").str() == "struct") {
            const std::string ref = t.at("ref").str();
            if (!ref.empty()) reaches[from].push_back(ref);
            return;
        }
        if (!t.at("elem").null()) note_reach(from, t.at("elem"));
    }

    // words is every word written about the types an op can see, keyed the way
    // the schema builder looks them up: the declaration's name, a dot, and the
    // member's wire name.
    void words(const Value& t, std::map<std::string, std::string>* out,
               std::set<std::string>* seen) const {
        if (t.null()) return;
        if (t.at("kind").str() != "struct") {
            if (!t.at("elem").null()) words(t.at("elem"), out, seen);
            return;
        }
        const std::string ref = t.at("ref").str();
        if (ref.empty() || seen->count(ref)) return;
        seen->insert(ref);
        if (auto it = prose.find(ref); it != prose.end()) {
            for (const auto& [k, v] : it->second) (*out)[k] = v;
        }
        if (auto it = reaches.find(ref); it != reaches.end()) {
            for (const std::string& next : it->second) {
                if (seen->count(next)) continue;
                Value r = Value::record();
                r.set("kind", Value::text("struct"));
                r.set("ref", Value::text(next));
                words(r, out, seen);
            }
        }
    }

    // members is the body a reference already has, which is how a base's fields
    // reach the object that absorbs them.
    std::vector<Value> members(const Value& ref) {
        std::vector<Value> out;
        const std::string key = ref.at("ref").str();
        for (const Value& f : structs.at(key).at("body").items()) out.push_back(f);
        return out;
    }

    void field(CXCursor c, const std::string& key, const std::string& record, Value* body,
               Value* own) {
        const std::string name = str(clang_getCursorSpelling(c));
        const auto all = notes(c);
        const bool hidden = clang_getCXXAccessSpecifier(c) != CX_CXXPublic;

        Value t = typ(clang_getCursorType(c));
        std::string v;
        if (note(all, "schema", &v)) {
            // A value that states its own wire form is not described by what it
            // is made of — the one thing a source pass cannot work out, and the
            // one thing a declaration can simply say.
            Value schema;
            if (!zip::json::read(v, &schema)) fail(name + ": ZIP_SCHEMA is not JSON: " + v);
            t.set("states", Value::boolean(true));
            t.set("schema", schema);
        }
        if (note(all, "text", &v)) t.set("text", Value::boolean(true));

        Value f = Value::record();
        f.set("name", Value::text(name));
        std::string wire = name;
        if (note(all, "json", &v)) wire = v;
        f.set("json", Value::text(wire));
        std::string url = wire;
        if (note(all, "url", &v)) url = v;
        f.set("url", Value::text(url));
        if (note(all, "header", &v)) f.set("header", Value::text(v));
        std::string ignored;
        if (note(all, "required", &ignored)) f.set("required", Value::boolean(true));
        if (note(all, "omit", &ignored)) f.set("omit", Value::boolean(true));
        f.set("type", t);

        Value o = f;
        if (hidden) o.set("private", Value::boolean(true));
        own->push(std::move(o));
        // A member the BODY does not carry is still a member: it may ride a
        // header or the URL, and a projection that never saw it would describe
        // an op that cannot be called. What "-" means is where it goes, not
        // whether it exists.
        if (!hidden) body->push(std::move(f));

        if (!hidden && wire != "-") {
            if (const std::string doc = comment(c); !doc.empty()) prose[key][record + "." + wire] = doc;
        }
        note_reach(key, t);
    }
};

// ---- the ops ----------------------------------------------------------------

const char* verb(const std::string& fn) {
    if (fn == "get") return "GET";
    if (fn == "post") return "POST";
    if (fn == "put") return "PUT";
    if (fn == "patch") return "PATCH";
    if (fn == "remove") return "DELETE";
    return nullptr;
}

struct Found {
    CXCursor path = clang_getNullCursor();
    CXCursor handler = clang_getNullCursor();
    // What the registration declared beyond the address: zip::status(201),
    // zip::id("forget"), zip::tags("items"). Read from the call, which is where
    // it is written and where Go writes the same thing.
    std::vector<std::pair<std::string, std::string>> options;
};

// value is what a literal argument IS, evaluated by the compiler rather than
// re-parsed: an integer or a string, whichever the option takes.
std::string value(CXCursor c) {
    CXEvalResult r = clang_Cursor_Evaluate(c);
    if (r == nullptr) return "";
    std::string out;
    switch (clang_EvalResult_getKind(r)) {
        case CXEval_Int:
            out = std::to_string(clang_EvalResult_getAsLongLong(r));
            break;
        case CXEval_StrLiteral:
        case CXEval_CFStr:
        case CXEval_ObjCStrLiteral: {
            const char* str = clang_EvalResult_getAsStr(r);
            if (str) out = str;
            break;
        }
        default:
            break;
    }
    clang_EvalResult_dispose(r);
    return out;
}

// arguments walks one registration's arguments for the two facts it states: the
// address, which must be a literal, and the handler, which carries its own
// types and its own prose.
void arguments(CXCursor call, Found* out) {
    struct State {
        Found* out;
        int index = 0;
    } s{out};
    clang_visitChildren(
        call,
        [](CXCursor kid, CXCursor, CXClientData d) {
            State* s = static_cast<State*>(d);
            if (s->index++ == 0) return CXChildVisit_Continue;  // the callee, not an argument
            struct Look {
                Found* out;
            } look{s->out};
            clang_visitChildren(
                kid,
                [](CXCursor deep, CXCursor, CXClientData dd) {
                    Look* l = static_cast<Look*>(dd);
                    if (clang_getCursorKind(deep) == CXCursor_CallExpr) {
                        const CXCursor to = clang_getCursorReferenced(deep);
                        const CXCursor where = clang_getCursorSemanticParent(to);
                        const std::string what = str(clang_getCursorSpelling(to));
                        if (!clang_Cursor_isNull(to) &&
                            clang_getCursorKind(where) == CXCursor_Namespace &&
                            str(clang_getCursorSpelling(where)) == "zip" &&
                            (what == "status" || what == "id" || what == "tags" ||
                             what == "summary" || what == "answers")) {
                            struct Arg {
                                std::string v;
                            } arg;
                            clang_visitChildren(
                                deep,
                                [](CXCursor a, CXCursor, CXClientData ad) {
                                    Arg* g = static_cast<Arg*>(ad);
                                    if (!g->v.empty()) return CXChildVisit_Break;
                                    g->v = value(a);
                                    return g->v.empty() ? CXChildVisit_Recurse : CXChildVisit_Break;
                                },
                                &arg);
                            l->out->options.emplace_back(what, arg.v);
                            return CXChildVisit_Break;
                        }
                    }
                    if (clang_getCursorKind(deep) == CXCursor_StringLiteral &&
                        clang_Cursor_isNull(l->out->path)) {
                        l->out->path = deep;
                        return CXChildVisit_Break;
                    }
                    const CXCursor ref = clang_getCursorReferenced(deep);
                    if (!clang_Cursor_isNull(ref) && clang_isDeclaration(clang_getCursorKind(ref)) &&
                        (clang_getCursorKind(ref) == CXCursor_CXXMethod ||
                         clang_getCursorKind(ref) == CXCursor_FunctionDecl) &&
                        clang_Cursor_isNull(l->out->handler)) {
                        l->out->handler = ref;
                        return CXChildVisit_Break;
                    }
                    return CXChildVisit_Recurse;
                },
                &look);
            if (clang_getCursorKind(kid) == CXCursor_StringLiteral && clang_Cursor_isNull(s->out->path)) {
                s->out->path = kid;
            }
            return CXChildVisit_Continue;
        },
        &s);
}

std::string literal(CXCursor c) {
    std::string raw = str(clang_getCursorSpelling(c));
    if (raw.size() >= 2 && raw.front() == '"' && raw.back() == '"') raw = raw.substr(1, raw.size() - 2);
    std::string out;
    for (std::size_t i = 0; i < raw.size(); ++i) {
        if (raw[i] == '\\' && i + 1 < raw.size()) {
            ++i;
            switch (raw[i]) {
                case 'n': out += '\n'; break;
                case 't': out += '\t'; break;
                case '"': out += '"'; break;
                case '\\': out += '\\'; break;
                default: out += raw[i];
            }
            continue;
        }
        out += raw[i];
    }
    return out;
}

struct Pass {
    Extract e;
    Value app = Value::record();
    std::string name, title, description, version;
};

CXChildVisitResult walk(CXCursor c, CXCursor, CXClientData d) {
    Pass* p = static_cast<Pass*>(d);
    if (!clang_Location_isFromMainFile(clang_getCursorLocation(c))) {
        return CXChildVisit_Recurse;
    }

    // The app names itself, and its own doc comment is the document's title and
    // description: one comment, one place, exactly as an op's is.
    if (clang_getCursorKind(c) == CXCursor_VarDecl &&
        str(clang_getTypeSpelling(clang_getCursorType(c))) == "zip::App") {
        // The name, then the release: the two literals the constructor takes.
        struct Lit {
            std::vector<std::string> all;
        } lit;
        clang_visitChildren(
            c,
            [](CXCursor kid, CXCursor, CXClientData dd) {
                Lit* l = static_cast<Lit*>(dd);
                if (clang_getCursorKind(kid) == CXCursor_StringLiteral) {
                    l->all.push_back(literal(kid));
                    return l->all.size() >= 2 ? CXChildVisit_Break : CXChildVisit_Continue;
                }
                return CXChildVisit_Recurse;
            },
            &lit);
        if (!lit.all.empty()) p->name = lit.all[0];
        if (lit.all.size() > 1) p->version = lit.all[1];
        const std::string doc = Extract::comment(c);
        const std::size_t nl = doc.find('\n');
        p->title = nl == std::string::npos ? doc : doc.substr(0, nl);
        if (nl != std::string::npos) {
            std::string rest = doc.substr(nl + 1);
            while (!rest.empty() && rest.front() == '\n') rest.erase(0, 1);
            p->description = rest;
        }
        return CXChildVisit_Continue;
    }

    if (clang_getCursorKind(c) != CXCursor_CallExpr) return CXChildVisit_Recurse;
    const CXCursor ref = clang_getCursorReferenced(c);
    if (clang_Cursor_isNull(ref)) return CXChildVisit_Recurse;
    const char* method = verb(str(clang_getCursorSpelling(ref)));
    if (method == nullptr) return CXChildVisit_Recurse;
    const CXCursor owner = clang_getCursorSemanticParent(ref);
    if (clang_getCursorKind(owner) != CXCursor_Namespace ||
        str(clang_getCursorSpelling(owner)) != "zip") {
        return CXChildVisit_Recurse;
    }

    Found f;
    arguments(c, &f);
    if (clang_Cursor_isNull(f.handler)) {
        fail("a registration names no handler at " + str(clang_getCursorSpelling(c)));
    }
    if (clang_Cursor_isNull(f.path)) {
        // An op with a computed path has no identity to document: no
        // operationId, no tool name, no command, no route. Refusing here is the
        // whole point of reading the source.
        CXSourceLocation loc = clang_getCursorLocation(c);
        CXFile file;
        unsigned line = 0, col = 0;
        clang_getFileLocation(loc, &file, &line, &col, nullptr);
        fail(str(clang_getFileName(file)) + ":" + std::to_string(line) +
             ": the path is not a literal, so this op has no address to be documented under");
    }

    Value op = Value::record();
    op.set("method", Value::text(method));
    op.set("path", Value::text(literal(f.path)));
    const std::string pkg = Extract::home(clang_getCursorSemanticParent(f.handler));
    if (!pkg.empty()) op.set("pkg", Value::text(pkg));

    const CXType in = clang_getArgType(clang_getCursorType(f.handler), 0);
    const CXType out = clang_getResultType(clang_getCursorType(f.handler));
    const CXType bare = clang_getUnqualifiedType(clang_getCanonicalType(clang_getNonReferenceType(in)));
    if (str(clang_getTypeSpelling(bare)) != "zip::None") {
        op.set("in", p->e.typ(in));
    } else {
        // An op that takes nothing. It is the ANONYMOUS empty declaration —
        // zip::None is how C++ spells "no input", not a type the document has
        // anything to say about — which is the same thing Go's *struct{} is.
        Value none = Value::record();
        none.set("kind", Value::text("struct"));
        none.set("ref", Value::text("none"));
        p->e.structs.put("none", Value::record());
        op.set("in", none);
    }
    if (out.kind != CXType_Void) op.set("out", p->e.typ(out));

    // What an op declares beyond its address: its id, its tags, the statuses it
    // may answer with, the headers it may set — stated at the registration, the
    // way Go states them, so one line says everything about one op.
    std::vector<std::string> marks;
    for (const auto& [what, v] : f.options) marks.push_back(what + ":" + v);
    std::string mark;
    if (note(marks, "id", &mark)) op.set("id", Value::text(mark));
    if (note(marks, "summary", &mark)) op.set("summary", Value::text(mark));
    if (note(marks, "tags", &mark)) {
        Value tags = Value::list();
        for (std::size_t i = 0, j; i <= mark.size(); i = j + 1) {
            j = mark.find(',', i);
            if (j == std::string::npos) j = mark.size();
            if (j > i) tags.push(Value::text(mark.substr(i, j - i)));
        }
        op.set("tags", std::move(tags));
    }
    if (note(marks, "status", &mark)) {
        Value all = Value::list();
        for (std::size_t i = 0, j; i <= mark.size(); i = j + 1) {
            j = mark.find(',', i);
            if (j == std::string::npos) j = mark.size();
            if (j > i) all.push(Value::raw(mark.substr(i, j - i)));
        }
        op.set("statuses", std::move(all));
    }
    if (note(marks, "answers", &mark)) {
        Value all = Value::list();
        for (std::size_t i = 0, j; i <= mark.size(); i = j + 1) {
            j = mark.find(',', i);
            if (j == std::string::npos) j = mark.size();
            if (j > i) all.push(Value::text(mark.substr(i, j - i)));
        }
        op.set("headers", std::move(all));
    }

    const std::string raw = Extract::comment(f.handler);
    std::map<std::string, std::string> fields;
    std::set<std::string> seen;
    p->e.words(op.at("in"), &fields, &seen);
    p->e.words(op.at("out"), &fields, &seen);
    if (!raw.empty() || !fields.empty()) {
        const Doc doc = split(raw);
        Value dv = Value::record();
        if (!doc.description.empty()) dv.set("description", Value::text(doc.description));
        if (!fields.empty()) {
            Value fv = Value::record();
            for (const auto& [k, v] : fields) fv.set(k, Value::text(v));
            dv.set("fields", std::move(fv));
        }
        if (!doc.example.empty()) dv.set("example", Value::raw(doc.example));
        if (!doc.response.empty()) dv.set("response", Value::raw(doc.response));
        op.set("doc", std::move(dv));
    }
    p->e.ops.push_back(std::move(op));
    return CXChildVisit_Continue;
}

}  // namespace

int main(int argc, char** argv) {
    std::string source, out, version, appName;
    std::vector<std::string> flags;
    for (int i = 1; i < argc; ++i) {
        const std::string a = argv[i];
        if (a == "-o" && i + 1 < argc) out = argv[++i];
        else if (a == "--version" && i + 1 < argc) version = argv[++i];
        else if (a == "--app" && i + 1 < argc) appName = argv[++i];
        else if (a == "--") { for (++i; i < argc; ++i) flags.push_back(argv[i]); }
        else if (a == "--db" && i + 1 < argc) {
            CXCompilationDatabase_Error err;
            CXCompilationDatabase db = clang_CompilationDatabase_fromDirectory(argv[++i], &err);
            if (err != CXCompilationDatabase_NoError) fail("no compile_commands.json in that directory");
            if (source.empty()) fail("--db must follow the source file");
            CXCompileCommands cmds = clang_CompilationDatabase_getCompileCommands(db, source.c_str());
            if (clang_CompileCommands_getSize(cmds) == 0) {
                fail("compile_commands.json does not know how to compile " + source);
            }
            CXCompileCommand cmd = clang_CompileCommands_getCommand(cmds, 0);
            const unsigned n = clang_CompileCommand_getNumArgs(cmd);
            for (unsigned k = 1; k < n; ++k) {
                const std::string arg = str(clang_CompileCommand_getArg(cmd, k));
                if (arg == "-c" || arg == "-o" || arg == source) continue;
                if (k + 1 < n && arg == "-o") { ++k; continue; }
                flags.push_back(arg);
            }
            clang_CompileCommands_dispose(cmds);
            clang_CompilationDatabase_dispose(db);
        } else source = a;
    }
    if (source.empty()) fail("usage: zipc <source.cpp> [--db <dir>] [--version v] [-o manifest.json] -- <clang flags>");

    std::vector<const char*> args;
    for (const std::string& f : flags) args.push_back(f.c_str());
    args.push_back("-fparse-all-comments");  // a doc comment is the spec; read every one

    CXIndex index = clang_createIndex(0, 0);
    CXTranslationUnit tu = nullptr;
    const CXErrorCode code = clang_parseTranslationUnit2(
        index, source.c_str(), args.data(), int(args.size()), nullptr, 0,
        CXTranslationUnit_SkipFunctionBodies == 0 ? CXTranslationUnit_None : CXTranslationUnit_None, &tu);
    if (code != CXError_Success) fail("could not parse " + source);

    bool broken = false;
    const unsigned diagnostics = clang_getNumDiagnostics(tu);
    for (unsigned i = 0; i < diagnostics; ++i) {
        CXDiagnostic d = clang_getDiagnostic(tu, i);
        if (clang_getDiagnosticSeverity(d) >= CXDiagnostic_Error) {
            std::fprintf(stderr, "zipc: %s\n", str(clang_formatDiagnostic(d, 0)).c_str());
            broken = true;
        }
        clang_disposeDiagnostic(d);
    }
    // A source that does not compile is a source whose types are guesses. The
    // manifest would be a document about a program that does not exist.
    if (broken) fail("the source does not compile; the manifest would describe nothing real");

    Pass pass;
    clang_visitChildren(clang_getTranslationUnitCursor(tu), walk, &pass);
    if (pass.e.ops.empty()) fail("no operations found in " + source);

    Value m = Value::record();
    m.set("name", Value::text(appName.empty() ? pass.name : appName));
    if (!pass.title.empty()) m.set("title", Value::text(pass.title));
    if (!pass.description.empty()) m.set("description", Value::text(pass.description));
    // The release the source states, or the one this build stamps: a version is
    // a fact about a release, and either place can be the one that knows it.
    if (version.empty()) version = pass.version;
    if (!version.empty()) m.set("version", Value::text(version));
    Value ops = Value::list();
    // Sorted by address, which is the order every projection reads them in.
    std::sort(pass.e.ops.begin(), pass.e.ops.end(), [](const Value& a, const Value& b) {
        if (a.at("path").str() != b.at("path").str()) return a.at("path").str() < b.at("path").str();
        return a.at("method").str() < b.at("method").str();
    });
    for (Value& op : pass.e.ops) ops.push(std::move(op));
    m.set("ops", std::move(ops));
    m.set("structs", pass.e.structs);

    const std::string body = m.write();
    if (out.empty() || out == "-") {
        std::fwrite(body.data(), 1, body.size(), stdout);
    } else {
        FILE* f = std::fopen(out.c_str(), "wb");
        if (!f) fail("cannot write " + out);
        std::fwrite(body.data(), 1, body.size(), f);
        std::fclose(f);
    }
    return 0;
}
