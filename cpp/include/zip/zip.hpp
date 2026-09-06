// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: BSD-3-Clause-Eco
//
// zip, in C++: the runtime half.
//
// A registered op is the source. zip::get(app, "/chain/bootstrapped",
// &Info::bootstrapped, &info) states one operation, and everything else — the
// REST route, the ZAP door, the OpenAPI document, the MCP tool, the CLI command
// and the .zap schema — is a projection of that registration and the doc comment
// above the handler. The projections are written at build time by zipc, which
// reads THIS source; nothing downstream is written by hand, and nothing here is
// declared twice.
//
// What this file holds is only what must run: the route table, the two doors
// (HTTP/1.1 and ZAP), and the binding of a request onto a handler's input. What
// a type IS — its fields, their wire names, how to read and write one — is
// generated from the same source into <app>.zip.hpp, so this file names no type
// of yours and there is no reflection anywhere.

#ifndef ZIP_ZIP_HPP_
#define ZIP_ZIP_HPP_

#include <arpa/inet.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <sys/socket.h>
#include <unistd.h>

#include <cstring>
#include <functional>
#include <utility>
#include <stdexcept>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

#include "zip/http.hpp"
#include "zip/json.hpp"
#include "zip/wire.hpp"

namespace zip {

// None is an op that takes nothing. It is the empty declaration, and the
// document describes it as the object with no members it is.
struct None {};

inline void read_json(const json::Value&, None&) {}
inline json::Value write_json(const None&) { return json::Value::record(); }

// How a value is read off a request and written into an answer, DECLARED here
// and defined by the generated <app>.zip.hpp beside each type it describes.
//
// Declared rather than defined, because what a type is made of is exactly what
// this language cannot ask at run time — and exactly what the manifest already
// knows. A build that has not generated its bindings still PARSES, which is what
// lets the extractor read a source whose generated half does not exist yet; it
// simply does not link, which is the honest failure for a missing definition.
template <class T>
void read_json(const json::Value&, T&);
template <class T>
json::Value write_json(const T&);

// Error is a handler refusing, with the status the refusal deserves. It is what
// a handler throws instead of returning a second value: the answer is the
// output, and a failure is not one.
struct Error : std::runtime_error {
    Error(int code, std::string what) : std::runtime_error(std::move(what)), status(code) {}
    explicit Error(std::string what) : std::runtime_error(std::move(what)), status(400) {}
    int status;
};

// Run is one op with its types already resolved: an input object in, an answer
// out. Every transport calls this and nothing else.
using Run = std::function<json::Value(const json::Value&)>;

namespace detail {

// sig reads a handler's input and output types off the handler itself, which is
// the same thing the extractor does to write the manifest — one declaration,
// read twice, never spelled twice.
template <class F>
struct sig;
template <class S, class Out, class In>
struct sig<Out (S::*)(const In&)> {
    using in = In;
    using out = Out;
};
template <class S, class Out, class In>
struct sig<Out (S::*)(const In&) const> {
    using in = In;
    using out = Out;
};
template <class Out, class In>
struct sig<Out (*)(const In&)> {
    using in = In;
    using out = Out;
};

}  // namespace detail

// Option is what an op declares beyond its address and its types: the status it
// answers with, the name it carries on every surface, what it is grouped under,
// and the headers it may set. It is stated at the REGISTRATION, where Go states
// it, so one line says everything about one op and the pass that reads the
// source reads it from there.
//
// Only the status changes what the service DOES; the rest is what the document,
// the tool list and the CLI call it. They are one type all the same, because
// they are one idea — a fact about the op that its signature cannot carry — and
// two would mean two places to look.
struct Option {
    enum class What { Status, Id, Tags, Summary, Answers } what = What::Status;
    int number = 0;
    std::string text;
};

inline Option status(int code) { return Option{Option::What::Status, code, {}}; }
inline Option id(std::string name) { return Option{Option::What::Id, 0, std::move(name)}; }
inline Option tags(std::string list) { return Option{Option::What::Tags, 0, std::move(list)}; }
inline Option summary(std::string line) { return Option{Option::What::Summary, 0, std::move(line)}; }
inline Option answers(std::string list) { return Option{Option::What::Answers, 0, std::move(list)}; }

// Route is one address the router matches on. segs is the pattern already split,
// because a match is a walk over segments and splitting it per request would be
// work the registration already did.
struct Route {
    std::string method;
    std::string path;
    std::vector<std::string> segs;
    Run run;
    // The status a SUCCESSFUL answer carries. Zero means the default, which is
    // 200 — an op that declares 201 and answers 200 is a service its own
    // document does not describe.
    int status = 0;
};

inline std::vector<std::string> split(std::string_view path) {
    std::vector<std::string> out;
    std::size_t i = 0;
    while (i < path.size()) {
        if (path[i] == '/') { ++i; continue; }
        const std::size_t j = path.find('/', i);
        out.emplace_back(path.substr(i, j == std::string_view::npos ? j : j - i));
        if (j == std::string_view::npos) break;
        i = j + 1;
    }
    return out;
}

class App {
  public:
    // An app is what it is called and, when it has one, what release this is.
    // Both are read by the pass that writes the manifest, and the app's own doc
    // comment is the document's title and description — one comment, one place,
    // exactly as an op's is.
    explicit App(std::string name, std::string version = {})
        : name_(std::move(name)), version_(std::move(version)) {}

    const std::string& name() const { return name_; }
    const std::string& version() const { return version_; }
    const std::vector<Route>& routes() const { return routes_; }

    void add(std::string method, std::string path, Run run, std::vector<Option> opts = {}) {
        Route r;
        r.segs = split(path);
        r.method = std::move(method);
        r.path = std::move(path);
        r.run = std::move(run);
        for (const Option& o : opts) {
            if (o.what == Option::What::Status) r.status = o.number;
        }
        routes_.push_back(std::move(r));
    }

    // answer runs one request, whatever carried it. It is the ONE seam: the
    // HTTP door and the ZAP door both arrive here, so an op cannot behave one
    // way over one transport and another way over the other.
    int answer(std::string_view method, std::string_view target, std::string_view body,
               std::string* out) const {
        return answer(method, target, body, {}, out);
    }

    // The same, told what the request carried in its headers. A field that
    // declares one is part of the op's contract — the document names it — so
    // the binder has to fill it, or the contract is a claim about nothing.
    int answer(std::string_view method, std::string_view target, std::string_view body,
               const std::vector<std::pair<std::string, std::string>>& headers,
               std::string* out) const {
        std::string_view path = target;
        std::string_view query;
        if (const std::size_t q = target.find('?'); q != std::string_view::npos) {
            path = target.substr(0, q);
            query = target.substr(q + 1);
        }
        const std::vector<std::string> segs = split(path);

        for (const Route& r : routes_) {
            if (r.method != method) continue;
            json::Value in = json::Value::record();
            if (!match(r, segs, &in)) continue;

            // The body first, then the URL over it: the URL is the addressing
            // authority, so a value the path carries wins over a value the body
            // repeated. Go binds the same way round, for the same reason.
            json::Value merged = json::Value::record();
            if (!body.empty()) {
                json::Value parsed;
                if (!json::read(body, &parsed)) {
                    *out = problem(400, "the body is not JSON");
                    return 400;
                }
                if (parsed.kind() == json::Kind::Record) merged = std::move(parsed);
            }
            for (const auto& [k, v] : headers) merged.set(k, json::Value::text(v));
            for (const auto& [k, v] : fields(query)) merged.set(k, json::Value::text(v));
            for (const auto& [k, v] : in.body()) merged.set(k, v);

            try {
                *out = r.run(merged).write();
                return r.status == 0 ? 200 : r.status;
            } catch (const Error& e) {
                *out = problem(e.status, e.what());
                return e.status;
            } catch (const std::exception& e) {
                *out = problem(500, e.what());
                return 500;
            }
        }
        *out = problem(404, reason(404));
        return 404;
    }

    // listen serves this app on every address given. The SCHEME is a value, not
    // a method: a bare address is ZAP, which is the primary, and http:// is the
    // one every browser speaks. Same routes, same handlers, same answers.
    void listen(const std::vector<std::string>& addrs) const {
        std::vector<std::thread> doors;
        for (const std::string& a : addrs) {
            doors.emplace_back([this, a]() {
                if (a.rfind("http://", 0) == 0) {
                    serve(a.substr(7), false);
                } else {
                    serve(a, true);
                }
            });
        }
        for (std::thread& t : doors) t.join();
    }

    // problem is a refusal as RFC 7807 writes one, which is what a zip service
    // answers with in any language: the same four members, in the order a sorted
    // encoder puts them, so two implementations of one op refuse identically.
    static std::string problem(int status, std::string_view detail) {
        return std::string(R"({"detail":")") + json::escape(detail) + R"(","status":)" +
               std::to_string(status) + R"(,"title":")" + std::string(reason(status)) +
               R"(","type":"about:blank"})";
    }

  private:
    // match walks one pattern against the request's segments, filling in what the
    // path names. ":name" takes one segment; "*" takes the rest.
    static bool match(const Route& r, const std::vector<std::string>& segs, json::Value* in) {
        std::size_t i = 0;
        for (; i < r.segs.size(); ++i) {
            const std::string& want = r.segs[i];
            if (want == "*" || want == "+") {
                std::string rest;
                for (std::size_t j = i; j < segs.size(); ++j) {
                    if (j > i) rest += "/";
                    rest += segs[j];
                }
                if (want == "+" && rest.empty()) return false;
                in->set("*1", json::Value::text(rest));
                return true;
            }
            if (i >= segs.size()) return false;
            if (want.size() > 1 && want[0] == ':') {
                std::string name = want.substr(1);
                if (const std::size_t c = name.find_first_of("?<"); c != std::string::npos) {
                    name = name.substr(0, c);
                }
                in->set(name, json::Value::text(segs[i]));
                continue;
            }
            if (want != segs[i]) return false;
        }
        return i == segs.size();
    }

    // fields reads a query string into name/value pairs, percent-decoded.
    static std::vector<std::pair<std::string, std::string>> fields(std::string_view query) {
        std::vector<std::pair<std::string, std::string>> out;
        while (!query.empty()) {
            std::string_view one = query;
            if (const std::size_t amp = query.find('&'); amp != std::string_view::npos) {
                one = query.substr(0, amp);
                query = query.substr(amp + 1);
            } else {
                query = {};
            }
            const std::size_t eq = one.find('=');
            if (eq == std::string_view::npos) continue;
            out.emplace_back(unescape(one.substr(0, eq)), unescape(one.substr(eq + 1)));
        }
        return out;
    }

    static std::string unescape(std::string_view s) {
        std::string out;
        out.reserve(s.size());
        for (std::size_t i = 0; i < s.size(); ++i) {
            if (s[i] == '+') { out += ' '; continue; }
            if (s[i] == '%' && i + 2 < s.size()) {
                auto hex = [](char c) -> int {
                    if (c >= '0' && c <= '9') return c - '0';
                    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
                    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
                    return -1;
                };
                const int hi = hex(s[i + 1]), lo = hex(s[i + 2]);
                if (hi >= 0 && lo >= 0) {
                    out += char(hi * 16 + lo);
                    i += 2;
                    continue;
                }
            }
            out += s[i];
        }
        return out;
    }

    // serve binds one address and answers on it until the socket is closed. zap
    // says which door: length-prefixed ZAP frames, or HTTP/1.1 text.
    void serve(const std::string& addr, bool zap) const {
        int port = 0;
        std::string host = "0.0.0.0";
        if (const std::size_t c = addr.rfind(':'); c != std::string::npos) {
            if (c > 0) host = addr.substr(0, c);
            port = std::atoi(addr.c_str() + c + 1);
        }
        const int fd = ::socket(AF_INET, SOCK_STREAM, 0);
        if (fd < 0) return;
        int one = 1;
        ::setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &one, sizeof(one));
        sockaddr_in sa{};
        sa.sin_family = AF_INET;
        sa.sin_port = htons(std::uint16_t(port));
        sa.sin_addr.s_addr = host == "0.0.0.0" || host.empty() ? INADDR_ANY : ::inet_addr(host.c_str());
        if (::bind(fd, reinterpret_cast<sockaddr*>(&sa), sizeof(sa)) != 0) { ::close(fd); return; }
        if (::listen(fd, 128) != 0) { ::close(fd); return; }
        for (;;) {
            const int c = ::accept(fd, nullptr, nullptr);
            if (c < 0) break;
            std::thread([this, c, zap]() {
                zap ? zapConn(c) : httpConn(c);
                ::close(c);
            }).detach();
        }
        ::close(fd);
    }

    void httpConn(int c) const {
        std::string buf;
        char chunk[16384];
        for (;;) {
            const ssize_t n = ::recv(c, chunk, sizeof(chunk), 0);
            if (n <= 0) return;
            buf.append(chunk, std::size_t(n));
            for (;;) {
                http::Request req;
                const std::ptrdiff_t used = http::parse(buf, &req);
                if (used == 0) break;
                if (used < 0) return;
                std::string body;
                const int status = answer(req.method, req.target, req.body, req.headers, &body);
                const std::string out =
                    http::answer(status, reason(status), body, req.keepalive,
                                 status >= 400 ? "application/problem+json" : "application/json");
                if (!send(c, out)) return;
                buf.erase(0, std::size_t(used));
                if (!req.keepalive) return;
            }
        }
    }

    void zapConn(int c) const {
        std::string buf;
        char chunk[16384];
        for (;;) {
            const ssize_t n = ::recv(c, chunk, sizeof(chunk), 0);
            if (n <= 0) return;
            buf.append(chunk, std::size_t(n));
            for (;;) {
                if (buf.size() < 4) break;
                const std::uint32_t len = (std::uint32_t(std::uint8_t(buf[0])) << 24) |
                                          (std::uint32_t(std::uint8_t(buf[1])) << 16) |
                                          (std::uint32_t(std::uint8_t(buf[2])) << 8) |
                                          std::uint32_t(std::uint8_t(buf[3]));
                if (buf.size() < 4 + std::size_t(len)) break;
                const auto* p = reinterpret_cast<const std::uint8_t*>(buf.data()) + 4;
                std::vector<std::uint8_t> frame;
                zap::Message msg;
                wire::Request req;
                std::string body;
                int status = 400;
                if (zap::Message::parse({p, std::size_t(len)}, &msg, nullptr) && wire::read(msg, &req)) {
                    status = answer(req.method, req.target,
                                    std::string_view(reinterpret_cast<const char*>(req.body.data()),
                                                     req.body.size()),
                                    &body);
                } else {
                    body = R"({"error":"not a request frame"})";
                }
                const std::string head = wire::headers(
                    {{"Content-Type", status >= 400 ? "application/problem+json" : "application/json"}});
                frame = wire::answer(std::uint16_t(status), reason(status), head, body);
                std::string out(4, '\0');
                const std::uint32_t m = std::uint32_t(frame.size());
                out[0] = char(m >> 24); out[1] = char(m >> 16); out[2] = char(m >> 8); out[3] = char(m);
                out.append(reinterpret_cast<const char*>(frame.data()), frame.size());
                if (!send(c, out)) return;
                buf.erase(0, 4 + std::size_t(len));
            }
        }
    }

    static bool send(int c, const std::string& out) {
        std::size_t sent = 0;
        while (sent < out.size()) {
            const ssize_t w = ::send(c, out.data() + sent, out.size() - sent, 0);
            if (w <= 0) return false;
            sent += std::size_t(w);
        }
        return true;
    }

    static std::string_view reason(int status) {
        switch (status) {
            case 200: return "OK";
            case 201: return "Created";
            case 202: return "Accepted";
            case 204: return "No Content";
            case 400: return "Bad Request";
            case 401: return "Unauthorized";
            case 403: return "Forbidden";
            case 404: return "Not Found";
            case 500: return "Internal Server Error";
        }
        return "OK";
    }

    std::string name_;
    std::string version_;
    std::vector<Route> routes_;
};

// The five registrars. Each states one op: its method, its address, and the
// handler that answers it. The handler's own types are read off the handler, so
// an op is declared exactly once and in one place — which is what makes the
// registration line the source every projection reduces over.
namespace detail {

template <class F, class S>
Run bind(F fn, S* self) {
    using In = typename sig<F>::in;
    return [fn, self](const json::Value& v) {
        In in{};
        read_json(v, in);
        return write_json((self->*fn)(in));
    };
}

template <class F>
Run bind(F fn) {
    using In = typename sig<F>::in;
    return [fn](const json::Value& v) {
        In in{};
        read_json(v, in);
        return write_json(fn(in));
    };
}

}  // namespace detail

template <class F, class S, class... Opts>
void get(App& app, std::string path, F fn, S* self, Opts&&... opts) {
    app.add("GET", std::move(path), detail::bind(fn, self), {std::forward<Opts>(opts)...});
}
template <class F, class S, class... Opts>
void post(App& app, std::string path, F fn, S* self, Opts&&... opts) {
    app.add("POST", std::move(path), detail::bind(fn, self), {std::forward<Opts>(opts)...});
}
template <class F, class S, class... Opts>
void put(App& app, std::string path, F fn, S* self, Opts&&... opts) {
    app.add("PUT", std::move(path), detail::bind(fn, self), {std::forward<Opts>(opts)...});
}
template <class F, class S, class... Opts>
void patch(App& app, std::string path, F fn, S* self, Opts&&... opts) {
    app.add("PATCH", std::move(path), detail::bind(fn, self), {std::forward<Opts>(opts)...});
}
template <class F, class S, class... Opts>
void remove(App& app, std::string path, F fn, S* self, Opts&&... opts) {
    app.add("DELETE", std::move(path), detail::bind(fn, self), {std::forward<Opts>(opts)...});
}

}  // namespace zip

#endif  // ZIP_ZIP_HPP_
