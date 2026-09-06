// SPDX-License-Identifier: BSD-3-Clause-Eco

#include "schema.hpp"

namespace zipcpp {
namespace {

// settle puts the file in the order zip's own renderer puts it: structs and
// interfaces by name, and every register of the ledger by what it is about. An
// order that depended on which header was included first would make a diff of
// two builds unreadable, and the schema is a file people read.
//
// Methods keep the order the walk gave them, which is by the name the AUTHOR
// wrote — sorting them here would sort by the name the IDL's lexer can spell,
// and an op whose spelling had to change would move.
void settle(schema& s) {
  std::sort(s.declarations.begin(), s.declarations.end(),
            [](const declaration& a, const declaration& b) { return a.name < b.name; });
  std::sort(s.services.begin(), s.services.end(),
            [](const service& a, const service& b) { return a.name < b.name; });
  std::sort(s.gaps.begin(), s.gaps.end(), [](const gap& a, const gap& b) {
    return a.op != b.op ? a.op < b.op : a.field < b.field;
  });
  std::sort(s.opaque.begin(), s.opaque.end(), [](const opacity& a, const opacity& b) {
    return a.decl != b.decl ? a.decl < b.decl : a.field < b.field;
  });
  std::sort(s.coded.begin(), s.coded.end(), [](const coded& a, const coded& b) {
    return a.decl != b.decl ? a.decl < b.decl : a.field < b.field;
  });
  std::sort(s.renamed.begin(), s.renamed.end(),
            [](const rename& a, const rename& b) { return a.op < b.op; });
}

std::string pad(const std::string& s, size_t w) {
  std::string out = s;
  out.append(w > s.size() ? w - s.size() : 0, ' ');
  return out;
}

// ledger states what the schema above does NOT carry.
//
// It lives in the same file rather than beside it because a second artifact is
// a second thing to keep in step, and a reader should learn the cost in the same
// breath as the contract. A schema that listed only what crosses would be true
// and would still mislead.
void ledger(std::ostringstream& b, const schema& s) {
  if (s.gaps.empty() && s.opaque.empty() && s.coded.empty() && s.renamed.empty()) return;
  b << "# ---------------------------------------------------------------------\n";
  b << "# " << s.ops() << " op(s) here. What follows is what this schema does not carry.\n";

  if (!s.gaps.empty()) {
    b << "#\n# blocked (" << s.gaps.size()
      << ") — the op is absent; the field has no wire form:\n";
    for (const auto& g : s.gaps)
      b << "#   " << g.op << "  " << g.field << "  " << g.type << "  (" << g.cause << ")\n";
  }
  if (!s.opaque.empty()) {
    b << "#\n# opaque (" << s.opaque.size() << ") — crosses, arrives without its name:\n";
    for (const auto& o : s.opaque)
      b << "#   " << o.decl << "." << o.field << "  " << o.type
        << (o.list ? " (list element)" : "") << "\n";
  }
  if (!s.coded.empty()) {
    b << "#\n# coded (" << s.coded.size()
      << ") — needs a generated codec; the reflective one refuses:\n";
    for (const auto& c : s.coded)
      b << "#   " << c.decl << "." << c.field << "  " << c.type << "\n";
  }
  if (!s.renamed.empty()) {
    b << "#\n# renamed (" << s.renamed.size()
      << ") — spelled differently here than on every other surface:\n";
    for (const auto& r : s.renamed) b << "#   " << r.op << "  ->  " << r.method << "\n";
  }
}

}  // namespace

std::string text(const schema& in) {
  schema s = in;
  settle(s);

  std::ostringstream b;
  b << "# Generated from C++ declarations. Do not edit.\n";
  b << "# Every struct and method below is derived from one service's member\n";
  b << "# functions, and every offset from the layout the op-call plane encodes\n";
  b << "# against.\n\n";
  b << "package " << s.package << "\n\n";

  for (const auto& d : s.declarations) {
    for (const auto& line : lines(d.doc)) b << "# " << line << "\n";
    b << "struct " << d.name << " {\n";
    size_t nw = 0, tw = 0;
    for (const auto& f : d.fields) {
      nw = std::max(nw, f.name.size());
      tw = std::max(tw, say(f.type).size());
    }
    for (const auto& f : d.fields) {
      for (const auto& line : lines(f.doc)) b << "    # " << line << "\n";
      b << "    " << pad(f.name, nw) << " " << pad(say(f.type), tw) << " @" << f.offset << "\n";
    }
    b << "}\n\n";
  }

  for (const auto& svc : s.services) {
    for (const auto& line : lines(svc.doc)) b << "# " << line << "\n";
    b << "interface " << svc.name << " {\n";
    for (const auto& m : svc.methods) {
      for (const auto& line : lines(m.doc)) b << "    # " << line << "\n";
      b << "    " << m.name << "(";
      if (!m.request.empty()) b << "req: " << m.request;
      b << ")";
      if (!m.reply.empty()) b << " returns (rep: " << m.reply << ")";
      b << "\n";
    }
    b << "}\n\n";
  }

  ledger(b, s);

  std::string out = b.str();
  size_t end = out.find_last_not_of('\n');
  return end == std::string::npos ? out : out.substr(0, end + 1) + "\n";
}

}  // namespace zipcpp
