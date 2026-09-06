// SPDX-License-Identifier: BSD-3-Clause-Eco
//
// The .zap a service declares: the wire rule, the model, and the text.
//
// Nothing here knows what Clang is. The walk decides WHAT a service declares;
// this decides where each value SITS and how the file reads, and it does so
// against types that are already reduced to the eleven scalars, text, bytes,
// a fixed run, a list and a nested value. Two concerns, two files: the wire
// rule is the part a reader has to be able to check line by line against the
// encoder it must agree with, and burying it in an AST visitor would hide it.
//
// # The rule
//
// A field is aligned to its own width and takes that many bytes; the fixed area
// is rounded up to eight. bytes_fixed[N] is the exception, and the reason
// alignment is stated apart from width: those N bytes are INLINE and align to 1,
// so a 32-byte seal follows a u32 at offset 4 rather than at 32.
//
// A nested value is EIGHT bytes and is spelled `bytes`, not four bytes spelled
// `struct`. That is what it is on this wire: the encoder writes the whole nested
// message into an {offset,length} slot. The IDL's own `struct` is a four-byte
// relative offset, so naming the type here would state a width nobody writes and
// move every field after it.
//
// This rule is stated in Go too, by the encoder every service actually speaks
// (internal/zapenc.LayoutOf). Two statements of one rule is one more than there
// should be, and it is not free: what keeps them together is that zip.ReadZAP
// derives the layout again from the schema this emits and REFUSES any field
// whose stated offset differs. So a drift here is a build failure over there,
// named field by field, rather than a wire two peers disagree about.

#ifndef ZIPCPP_SCHEMA_HPP
#define ZIPCPP_SCHEMA_HPP

#include <algorithm>
#include <cstdint>
#include <map>
#include <set>
#include <sstream>
#include <string>
#include <vector>

namespace zipcpp {

// kind is every shape the wire has. It is closed: a value that is none of these
// cannot cross, and the walk says so by name rather than choosing the nearest.
enum class kind {
  boolean,
  i8, i16, i32, i64,
  u8, u16, u32, u64,
  f32, f64,
  text,
  bytes,
  fixed,   // bytes_fixed[n]
  list,    // list<elem>
  nested,  // a struct, carried as bytes
};

// type is one field's wire type: a kind, and the detail two kinds carry.
struct type {
  zipcpp::kind kind = zipcpp::kind::bytes;
  int n = 0;                 // fixed: the run's length
  zipcpp::kind elem = zipcpp::kind::bytes;  // list: the element's kind
  int elemn = 0;             // list of fixed: the element run's length
  std::string decl;          // nested, or a list of nested: the struct's name
};

inline const char* spell(kind k) {
  switch (k) {
    case kind::boolean: return "bool";
    case kind::i8:      return "i8";
    case kind::i16:     return "i16";
    case kind::i32:     return "i32";
    case kind::i64:     return "i64";
    case kind::u8:      return "u8";
    case kind::u16:     return "u16";
    case kind::u32:     return "u32";
    case kind::u64:     return "u64";
    case kind::f32:     return "f32";
    case kind::f64:     return "f64";
    case kind::text:    return "text";
    case kind::bytes:   return "bytes";
    case kind::fixed:   return "bytes_fixed";
    case kind::list:    return "list";
    // A nested value crosses as a complete message inside an {offset,length}
    // slot, which is what `bytes` names. See the note at the top of this file.
    case kind::nested:  return "bytes";
  }
  return "bytes";
}

// width is the fixed-area size of one field. Everything variable — text, bytes,
// a list, a nested value — carries an offset and a length, which is eight bytes.
inline int width(kind k, int n) {
  switch (k) {
    case kind::boolean: case kind::i8: case kind::u8:  return 1;
    case kind::i16: case kind::u16:                    return 2;
    case kind::i32: case kind::u32: case kind::f32:    return 4;
    case kind::fixed:                                  return n;
    default:                                           return 8;
  }
}

// align is where the slot may begin: its own width, except a fixed run, whose
// bytes are inline and begin anywhere.
inline int align(kind k, int n) { return k == kind::fixed ? 1 : width(k, n); }

inline int round(int off, int to) { return (off + to - 1) & ~(to - 1); }

// how a type reads in the schema text.
inline std::string say(const type& t) {
  switch (t.kind) {
    case kind::fixed: return std::string("bytes_fixed[") + std::to_string(t.n) + "]";
    case kind::list: {
      std::string e = t.elem == kind::fixed
          ? std::string("bytes_fixed[") + std::to_string(t.elemn) + "]"
          : spell(t.elem);
      return "list<" + e + ">";
    }
    default: return spell(t.kind);
  }
}

// field is one slot: a name, a type, and the byte it begins at.
struct field {
  std::string name;
  zipcpp::type type;
  int offset = 0;
  std::string doc;  // the `///` on the member itself
};

// declaration is one struct: its fields in declaration order, and the size of
// its fixed area.
struct declaration {
  std::string name;
  std::vector<field> fields;
  int size = 0;
  std::string doc;
};

// method is one operation: what it takes and what it answers, each a struct name
// or empty for a direction that carries nothing.
struct method {
  std::string name;
  std::string request;
  std::string reply;
  std::string doc;
};

// service is one declared interface.
struct service {
  std::string name;
  std::vector<method> methods;
  std::string doc;
};

// gap is one operation that is NOT in the schema, and why. Every register below
// is a thing the reader must be told without having to count.
struct gap {
  std::string op;
  std::string field;   // the member that cannot cross
  std::string type;    // its C++ type, as written
  std::string cause;
};

// opacity is one field whose bytes cross exactly and whose TYPE NAME is lost:
// a nested value, carried as a message inside an {offset,length} slot.
struct opacity {
  std::string decl;
  std::string field;
  std::string type;   // the C++ struct whose name is gone
  bool list = false;  // it is the ELEMENT of a list that is opaque
};

// coded is one field the schema states correctly and the reflective encoder
// refuses: bytes_fixed[N]. A generated codec carries it; reflection will not.
struct coded {
  std::string decl;
  std::string field;
  std::string type;
};

// rename is one method whose name the IDL's lexer cannot spell.
struct rename {
  std::string op;
  std::string method;
};

// schema is one .zap file.
struct schema {
  std::string package;
  std::vector<declaration> declarations;
  std::vector<service> services;
  std::vector<zipcpp::gap> gaps;
  std::vector<zipcpp::opacity> opaque;
  std::vector<zipcpp::coded> coded;
  std::vector<zipcpp::rename> renamed;

  int ops() const {
    int n = 0;
    for (const auto& s : services) n += static_cast<int>(s.methods.size());
    return n;
  }
};

// reserved is every word the grammar already spends. A struct called `text`
// parses as the scalar wherever it is referred to, so the field would silently
// change type; a struct called `list` would not parse at all.
inline bool reserved(const std::string& s) {
  static const std::set<std::string> words = {
      "bool", "u8", "u16", "u32", "u64", "i8", "i16", "i32", "i64", "f32", "f64",
      "text", "bytes", "bytes_fixed", "list",
      "package", "struct", "interface", "type", "returns"};
  return words.count(s) != 0;
}

// name reduces s to what the IDL's lexer accepts — a letter or '_' first, then
// letters, digits and '_' — and to nothing the parser reads as something else.
inline std::string name(const std::string& s) {
  std::string out;
  for (size_t i = 0; i < s.size(); i++) {
    char c = s[i];
    bool letter = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_';
    bool digit = c >= '0' && c <= '9';
    out += (letter || (digit && i > 0)) ? c : '_';
  }
  if (reserved(out)) out += '_';
  return out;
}

// lines splits a doc comment into the lines the schema writes, dropping the
// blank ones — a comment is prose and the file is a list of statements.
inline std::vector<std::string> lines(const std::string& doc) {
  std::vector<std::string> out;
  std::istringstream in(doc);
  std::string line;
  while (std::getline(in, line)) {
    size_t a = line.find_first_not_of(" \t\r");
    if (a == std::string::npos) continue;
    size_t b = line.find_last_not_of(" \t\r");
    out.push_back(line.substr(a, b - a + 1));
  }
  return out;
}

// text is the .zap file itself.
//
// The shape follows zip's own renderer so a service declared in Go and the same
// service declared here read the same: the header, the package, every struct in
// name order with its fields column-aligned, every interface in name order, and
// then the ledger of what the schema does NOT carry.
std::string text(const schema& s);

}  // namespace zipcpp

#endif
