// SPDX-License-Identifier: BSD-3-Clause-Eco
//
// zipcpp reads a C++ service and writes the .zap schema it declares.
//
// A C++ developer writes ordinary structs and a class whose member functions are
// its operations, marks that class ZIP_SERVICE, and documents both with `///`.
// That is the whole source. From the .zap this writes, one shared back end
// produces the OpenAPI document, the MCP tool list, the CLI, the SDKs and the
// docs — so the projections are not written per language and cannot disagree
// about what exists.
//
// # Why the AST and not a macro
//
// C++ has no proc-macro and no reflection at run time, so a declaration's
// STRUCTURE and its PROSE both have to be read where both still exist: the
// compiler's own parse. Clang attaches a `///` comment to the declaration it
// precedes, which is why the prose can be read from the member itself rather
// than from a block in the function's comment restating what the members are.
// A field's sentence lives on the field. There is no second place to look, and
// therefore no precedence rule.
//
// # What it decides, and what it does not
//
// It decides which declarations are operations, what each member's wire type is,
// and where each one sits. It does NOT decide what a method is called on the
// wire, what an operation's address is, or what any projection looks like: those
// belong to the schema and to the back end that reads it. The job ends at a
// correct .zap.

#include "schema.hpp"

#include "clang/AST/ASTConsumer.h"
#include "clang/AST/ASTContext.h"
#include "clang/AST/Attr.h"
#include "clang/AST/Decl.h"
#include "clang/AST/DeclCXX.h"
#include "clang/AST/DeclTemplate.h"
#include "clang/AST/RecursiveASTVisitor.h"
#include "clang/Frontend/CompilerInstance.h"
#include "clang/Frontend/FrontendAction.h"
#include "clang/Tooling/CommonOptionsParser.h"
#include "clang/Tooling/Tooling.h"
#include "llvm/Support/CommandLine.h"

#include <fstream>
#include <memory>
#include <sstream>

using namespace clang;

namespace {

llvm::cl::OptionCategory category("zipcpp options");
llvm::cl::opt<std::string> packageName(
    "package", llvm::cl::desc("the schema's package name"), llvm::cl::init("service"),
    llvm::cl::cat(category));
llvm::cl::opt<std::string> output(
    "o", llvm::cl::desc("write the schema here (default: standard output)"),
    llvm::cl::init(""), llvm::cl::cat(category));
llvm::cl::opt<bool> check(
    "check", llvm::cl::desc("compare against the file at -o and fail on any difference"),
    llvm::cl::init(false), llvm::cl::cat(category));

// annotation is the one thing a service declares beyond its members.
constexpr llvm::StringRef serviceAnnotation = "zip.service";

bool isService(const CXXRecordDecl* d) {
  for (const auto* a : d->specific_attrs<AnnotateAttr>())
    if (a->getAnnotation() == serviceAnnotation) return true;
  return false;
}

// prose is the `///` comment Clang attached to a declaration, formatted as the
// text the author wrote. Empty when there is none — a thing nobody documented is
// not a thing with an empty description.
std::string prose(const Decl* d, ASTContext& ctx) {
  const RawComment* rc = ctx.getRawCommentForDeclNoCache(d);
  if (rc == nullptr) return "";
  return rc->getFormattedText(ctx.getSourceManager(), ctx.getDiagnostics());
}

// inStd reports whether a declaration lives in namespace std, through however
// many inline namespaces an implementation puts in between — libstdc++ declares
// std::string inside std::__cxx11, and a name comparison would miss it.
bool inStd(const Decl* d) {
  for (const DeclContext* c = d->getDeclContext(); c != nullptr; c = c->getParent())
    if (const auto* ns = dyn_cast<NamespaceDecl>(c))
      if (ns->isStdNamespace()) return true;
  return false;
}

// specialization is the record's template arguments, when it has any: a
// std::vector is only a vector OF something, and that something is here.
const ClassTemplateSpecializationDecl* specialization(const CXXRecordDecl* d) {
  return dyn_cast<ClassTemplateSpecializationDecl>(d);
}

// problem is one member that cannot cross, and why. The whole list is collected
// rather than the first, because a work list that stops at its first line
// understates the work.
struct problem {
  std::string field;
  std::string type;
  std::string cause;
};

// The causes, in the words zip's own schema already uses for them.
constexpr const char* causeUnwirable = "no wire form";
constexpr const char* causeReaches = "reaches one";

// blame names the member a refusal is about: the struct, and the field inside it
// when the trouble is a field rather than the struct itself.
std::string blame(const std::string& decl, const problem& p) {
  return p.field.empty() ? decl : decl + "." + p.field;
}

// resolver turns C++ types into wire types, and a record into a laid-out struct.
//
// It is where the whole mapping lives: eleven scalars, text, bytes, a fixed run,
// a list and a nested value, and a named refusal for everything else. A type
// that is not one of those is not narrowed to the nearest — a value quietly
// crossing as something it is not is the failure a schema exists to prevent.
class resolver {
 public:
  explicit resolver(ASTContext& ctx) : ctx_(ctx) {}

  // wire is the wire type of q, or false with why.
  bool wire(QualType q, zipcpp::type& out, std::string& why) {
    QualType c = q.getNonReferenceType().getUnqualifiedType().getCanonicalType();

    if (const auto* bt = c->getAs<BuiltinType>()) return scalar(bt, out, why);

    if (const auto* et = c->getAs<EnumType>()) {
      // An enum crosses as the integer it is, which is what a named integer type
      // does in every other language that reaches this wire.
      const auto* bt = et->getDecl()->getIntegerType()->getAs<BuiltinType>();
      if (bt == nullptr) {
        why = "an enum whose underlying type is not an integer has " + std::string(causeUnwirable);
        return false;
      }
      return scalar(bt, out, why);
    }

    const CXXRecordDecl* rd = c->getAsCXXRecordDecl();
    if (rd == nullptr) {
      why = q.getAsString() + " has " + causeUnwirable;
      return false;
    }
    // A library type is read from its template arguments, which a specialization
    // carries whether or not anything required it to be complete: a vector of
    // vectors names an inner vector nothing ever instantiated, and refusing it
    // for being incomplete would report the wrong reason for the right answer.
    if (inStd(rd)) return standard(rd, q, out, why);

    if (rd->getDefinition() == nullptr) {
      why = q.getAsString() + " is declared and not defined here, so it has no members";
      return false;
    }
    rd = rd->getDefinition();

    // Anything else that is a class is a nested value: it crosses as a complete
    // message inside an {offset,length} slot. Its own members are checked here
    // and declared nowhere — see the opacity register.
    std::vector<zipcpp::field> fields;
    std::vector<problem> bad;
    int size = 0;
    if (!lay(rd, fields, size, bad)) {
      why = bad.empty()
                ? rd->getNameAsString() + " " + causeReaches
                : blame(rd->getNameAsString(), bad.front()) + " " + bad.front().cause;
      return false;
    }
    // A nested value with no members still takes its slot and crosses as an
    // empty message. Refusing it here would be this front end disagreeing with
    // the encoder about a shape the encoder accepts.
    out = zipcpp::type{zipcpp::kind::nested, 0, zipcpp::kind::bytes, 0, rd->getNameAsString()};
    return true;
  }

  // lay is the fields of one record, in declaration order, at the offsets this
  // wire puts them at. It answers false when any member cannot cross, and fills
  // bad with every one of them.
  bool lay(const CXXRecordDecl* rd, std::vector<zipcpp::field>& out, int& size,
           std::vector<problem>& bad) {
    if (busy_.count(rd) != 0) {
      bad.push_back({"", rd->getNameAsString(), "contains itself, so it has no fixed width"});
      return false;
    }
    busy_.insert(rd);

    for (const auto& b : rd->bases()) {
      bad.push_back({"", b.getType().getAsString(),
                     "a base class has no wire form; declare it as a member"});
    }

    int off = 0;
    for (const auto* f : rd->fields()) {
      // A non-public member is not part of the contract, exactly as an
      // unexported Go field is not: it is skipped, and it consumes no slot.
      if (f->getAccess() != AS_public) continue;
      if (f->isAnonymousStructOrUnion() || f->isBitField()) {
        bad.push_back({f->getNameAsString(), f->getType().getAsString(), causeUnwirable});
        continue;
      }
      zipcpp::type t;
      std::string why;
      if (!wire(f->getType(), t, why)) {
        bad.push_back({f->getNameAsString(), f->getType().getAsString(), why});
        continue;
      }
      off = zipcpp::round(off, zipcpp::align(t.kind, t.n));
      out.push_back(zipcpp::field{zipcpp::name(f->getNameAsString()), t, off, prose(f, ctx_)});
      off += zipcpp::width(t.kind, t.n);
    }
    size = zipcpp::round(off, 8);

    busy_.erase(rd);
    return bad.empty();
  }

 private:
  bool scalar(const BuiltinType* bt, zipcpp::type& out, std::string& why) {
    using zipcpp::kind;
    kind k;
    switch (bt->getKind()) {
      case BuiltinType::Bool:                              k = kind::boolean; break;
      case BuiltinType::Char_S: case BuiltinType::SChar:   k = kind::i8; break;
      case BuiltinType::Char_U: case BuiltinType::UChar:   k = kind::u8; break;
      case BuiltinType::Short:                             k = kind::i16; break;
      case BuiltinType::UShort:                            k = kind::u16; break;
      case BuiltinType::Int:                               k = kind::i32; break;
      case BuiltinType::UInt:                              k = kind::u32; break;
      case BuiltinType::Long: case BuiltinType::LongLong:  k = kind::i64; break;
      case BuiltinType::ULong: case BuiltinType::ULongLong: k = kind::u64; break;
      case BuiltinType::Float:                             k = kind::f32; break;
      case BuiltinType::Double:                            k = kind::f64; break;
      default:
        why = std::string(bt->getName(ctx_.getPrintingPolicy())) + " has " + causeUnwirable;
        return false;
    }
    out = zipcpp::type{k, 0, kind::bytes, 0, ""};
    return true;
  }

  // standard maps the three library types that have a wire form, and refuses the
  // rest by name. std::optional is the one worth saying out loud: the schema has
  // no way to state that a value may be absent, so a field that crossed as its
  // own type would arrive stated as required — a contract the author did not
  // write.
  bool standard(const CXXRecordDecl* rd, QualType q, zipcpp::type& out, std::string& why) {
    llvm::StringRef n = rd->getName();
    const auto* spec = specialization(rd);

    if (n == "basic_string" || n == "basic_string_view") {
      if (spec != nullptr && !spec->getTemplateArgs().asArray().empty()) {
        const auto* ch = spec->getTemplateArgs()[0].getAsType()->getAs<BuiltinType>();
        if (ch != nullptr && (ch->getKind() == BuiltinType::Char_S ||
                              ch->getKind() == BuiltinType::Char_U)) {
          out = zipcpp::type{zipcpp::kind::text, 0, zipcpp::kind::bytes, 0, ""};
          return true;
        }
      }
      why = q.getAsString() + " is text of something other than char, which has " + causeUnwirable;
      return false;
    }

    if (n == "vector" || n == "array") {
      if (spec == nullptr || spec->getTemplateArgs().asArray().empty()) {
        why = q.getAsString() + " has " + causeUnwirable;
        return false;
      }
      zipcpp::type elem;
      if (!wire(spec->getTemplateArgs()[0].getAsType(), elem, why)) return false;

      if (n == "array") {
        if (spec->getTemplateArgs().asArray().size() < 2) {
          why = q.getAsString() + " states no length";
          return false;
        }
        // A fixed run of bytes crosses inline. A fixed run of anything else has
        // no inline form on this wire; a list is how a sequence crosses.
        if (elem.kind != zipcpp::kind::u8) {
          why = "an array of " + spec->getTemplateArgs()[0].getAsType().getAsString() +
                " has " + causeUnwirable + "; use a vector";
          return false;
        }
        const llvm::APSInt& len = spec->getTemplateArgs()[1].getAsIntegral();
        out = zipcpp::type{zipcpp::kind::fixed, static_cast<int>(len.getExtValue()),
                           zipcpp::kind::bytes, 0, ""};
        return true;
      }

      if (elem.kind == zipcpp::kind::u8) {
        out = zipcpp::type{zipcpp::kind::bytes, 0, zipcpp::kind::bytes, 0, ""};
        return true;
      }
      if (elem.kind == zipcpp::kind::list) {
        why = "a list of lists has " + std::string(causeUnwirable) +
              "; wrap the inner one in a struct";
        return false;
      }
      out = zipcpp::type{zipcpp::kind::list, 0, elem.kind, elem.n, elem.decl};
      return true;
    }

    if (n == "optional") {
      // The schema states a type and never its absence, so a value that crossed
      // as its own type would arrive stated as required — a contract nobody
      // wrote. Declaring what an absent value IS (a sentinel, or a bool beside
      // it) is the author's to make, not this tool's.
      why = q.getAsString() + " may be absent, and the schema has no way to say so";
      return false;
    }
    why = q.getAsString() + " has " + causeUnwirable;
    return false;
  }

  ASTContext& ctx_;
  std::set<const CXXRecordDecl*> busy_;
};

// finder collects the annotated classes, in source order.
class finder : public RecursiveASTVisitor<finder> {
 public:
  bool VisitCXXRecordDecl(CXXRecordDecl* d) {
    if (!d->isThisDeclarationADefinition() || !isService(d)) return true;
    if (d->getDescribedClassTemplate() != nullptr) {
      // A template is a family of classes and an interface is one declared
      // service, so which member of the family this would be is a question the
      // marker does not answer. Naming that is better than emitting the pattern,
      // whose members have types no wire has.
      templates.push_back(d->getNameAsString());
      return true;
    }
    services.push_back(d);
    return true;
  }
  std::vector<CXXRecordDecl*> services;
  std::vector<std::string> templates;
};

// builder turns the found services into the schema they declare.
class builder {
 public:
  builder(ASTContext& ctx, zipcpp::schema& s) : ctx_(ctx), out_(s), r_(ctx) {}

  void run(const std::vector<CXXRecordDecl*>& services) {
    for (const auto* svc : services) {
      zipcpp::service iface;
      iface.name = zipcpp::name(svc->getNameAsString());
      iface.doc = prose(svc, ctx_);

      // A method that CAN spell its own name keeps it, so a neighbour never
      // takes a name its owner could have had.
      std::vector<const CXXMethodDecl*> ops;
      std::set<std::string> taken;
      std::set<std::string> seen;
      for (const auto* m : svc->methods()) {
        if (!operation(m)) continue;
        std::string id = m->getNameAsString();
        if (!seen.insert(id).second) {
          // A method's name is its operation's id, and every projection keys on
          // one: a second method of that name would take the first's place in
          // the document, the tool list and the SDK.
          out_.gaps.push_back({id, "(overload)", id,
                               "a method name is its operation's id, and this one is declared twice"});
          continue;
        }
        ops.push_back(m);
        if (zipcpp::name(id) == id) taken.insert(id);
      }
      // By the name the AUTHOR wrote, so an op whose spelling has to change for
      // the lexer still sits where its own name puts it.
      std::sort(ops.begin(), ops.end(), [](const CXXMethodDecl* a, const CXXMethodDecl* b) {
        return a->getNameAsString() < b->getNameAsString();
      });
      for (const auto* m : ops) method(iface, m, taken);
      if (!iface.methods.empty()) out_.services.push_back(iface);
    }
  }

 private:
  // operation reports whether a member function is one of the service's
  // operations: the ones the author wrote, not the ones the compiler did.
  static bool operation(const CXXMethodDecl* m) {
    return m->getAccess() == AS_public && !m->isStatic() && !m->isImplicit() &&
           !m->isOverloadedOperator() && m->getDescribedFunctionTemplate() == nullptr &&
           !isa<CXXConstructorDecl>(m) && !isa<CXXDestructorDecl>(m) &&
           !isa<CXXConversionDecl>(m);
  }

  void method(zipcpp::service& iface, const CXXMethodDecl* m, std::set<std::string>& taken) {
    std::string id = m->getNameAsString();
    QualType in;
    if (!request(m, in)) return;
    std::string req, rep;
    if (!payload(id, "request", in, req)) return;
    if (!payload(id, "reply", m->getReturnType(), rep)) return;

    std::string spelled = zipcpp::name(id);
    if (spelled != id) {
      for (int n = 2; taken.count(spelled) != 0; n++)
        spelled = zipcpp::name(id) + std::to_string(n);
      taken.insert(spelled);
      out_.renamed.push_back({id, spelled});
    }
    iface.methods.push_back(zipcpp::method{spelled, req, rep, prose(m, ctx_)});
  }

  // request is the one struct a method takes, left null for none. A method that
  // takes more than one is refused rather than folded into a struct this tool
  // invented: a ZAP method carries at most one payload per direction, and which
  // struct that is belongs to the author.
  bool request(const CXXMethodDecl* m, QualType& out) {
    if (m->getNumParams() == 0) return true;
    if (m->getNumParams() > 1) {
      out_.gaps.push_back({m->getNameAsString(), "(parameters)",
                           std::to_string(m->getNumParams()) + " parameters",
                           "a method carries at most one struct in each direction"});
      return false;
    }
    out = m->getParamDecl(0)->getType();
    return true;
  }

  // payload names the struct one direction carries, or leaves it empty for a
  // direction that carries nothing. It answers false when the direction cannot
  // be expressed at all, which costs the whole operation.
  bool payload(const std::string& op, const char* dir, QualType q, std::string& out) {
    if (q.isNull()) return true;
    QualType c = q.getNonReferenceType().getUnqualifiedType().getCanonicalType();
    if (c->isVoidType()) return true;

    const CXXRecordDecl* rd = c->getAsCXXRecordDecl();
    if (rd == nullptr || rd->getDefinition() == nullptr || inStd(rd)) {
      out_.gaps.push_back({op, dir, q.getAsString(),
                           "a payload is a struct declared in this source"});
      return false;
    }
    rd = rd->getDefinition();

    if (auto seen = declared_.find(rd); seen != declared_.end()) {
      if (seen->second.empty()) {
        out_.gaps.push_back({op, dir, rd->getNameAsString(), causeReaches});
        return false;
      }
      out = seen->second;
      return true;
    }

    std::vector<zipcpp::field> fields;
    std::vector<problem> bad;
    int size = 0;
    if (!r_.lay(rd, fields, size, bad)) {
      declared_[rd] = "";
      for (const auto& p : bad)
        out_.gaps.push_back({op, blame(rd->getNameAsString(), p), p.type, p.cause});
      return false;
    }
    if (fields.empty()) return true;  // a marker struct is the same absence

    std::string decl = unique(rd->getNameAsString());
    declared_[rd] = decl;
    for (const auto& f : fields) {
      if (f.type.kind == zipcpp::kind::nested)
        out_.opaque.push_back({decl, f.name, f.type.decl, false});
      if (f.type.kind == zipcpp::kind::list && f.type.elem == zipcpp::kind::nested)
        out_.opaque.push_back({decl, f.name, f.type.decl, true});
      if (f.type.kind == zipcpp::kind::fixed || f.type.elem == zipcpp::kind::fixed)
        out_.coded.push_back({decl, f.name, zipcpp::say(f.type)});
    }
    out_.declarations.push_back(zipcpp::declaration{decl, fields, size, prose(rd, ctx_)});
    out = decl;
    return true;
  }

  std::string unique(const std::string& raw) {
    std::string base = zipcpp::name(raw);
    std::string n = base;
    for (int i = 2; names_.count(n) != 0; i++) n = base + std::to_string(i);
    names_.insert(n);
    return n;
  }

  ASTContext& ctx_;
  zipcpp::schema& out_;
  resolver r_;
  std::map<const CXXRecordDecl*, std::string> declared_;
  std::set<std::string> names_;
};

// built is where the walk leaves its answer. A frontend action is made by a
// factory that takes nothing and returns nothing, so a translation unit's result
// has nowhere else to go; one tool run reads one set of sources and writes one
// schema, so there is one of these.
zipcpp::schema built;

class consumer : public ASTConsumer {
 public:
  void HandleTranslationUnit(ASTContext& ctx) override {
    finder f;
    f.TraverseDecl(ctx.getTranslationUnitDecl());
    for (const auto& t : f.templates)
      llvm::errs() << "zipcpp: " << t
                   << " is a class template, and an interface is one declared service; "
                      "mark a concrete class\n";
    builder(ctx, built).run(f.services);
  }
};

class action : public ASTFrontendAction {
 public:
  std::unique_ptr<ASTConsumer> CreateASTConsumer(CompilerInstance&, llvm::StringRef) override {
    return std::make_unique<consumer>();
  }
};

}  // namespace

int main(int argc, const char** argv) {
  auto parsed = tooling::CommonOptionsParser::create(argc, argv, category);
  if (!parsed) {
    llvm::errs() << llvm::toString(parsed.takeError());
    return 2;
  }
  tooling::ClangTool tool(parsed->getCompilations(), parsed->getSourcePathList());
  if (tool.run(tooling::newFrontendActionFactory<action>().get()) != 0) return 2;

  built.package = packageName;
  if (built.services.empty()) {
    llvm::errs() << "zipcpp: no service declared — mark one class ZIP_SERVICE and it "
                    "becomes an interface, its member functions its operations\n";
    return 1;
  }
  std::string out = zipcpp::text(built);

  if (check) {
    std::ifstream in(output.c_str());
    std::stringstream have;
    have << in.rdbuf();
    if (have.str() == out) return 0;
    llvm::errs() << "zipcpp: " << output
                 << " is not what these declarations describe; re-run without -check\n";
    return 1;
  }
  if (output.empty()) {
    llvm::outs() << out;
    return 0;
  }
  std::ofstream file(output.c_str());
  file << out;
  return file.good() ? 0 : 2;
}
