// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: BSD-3-Clause-Eco
//
// HTTP/1.1, read off a socket.
//
// The same ops answer here and over ZAP, so this file carries no routing, no
// binding and no idea what an op is: it turns bytes into a method, a target, a
// header list and a body, and turns a status and a body back into bytes. What
// happens in between is [zip::App::answer], once, for both transports.

#ifndef ZIP_HTTP_HPP_
#define ZIP_HTTP_HPP_

#include <cstddef>
#include <string>
#include <string_view>
#include <vector>

namespace zip::http {

struct Request {
    std::string method;
    std::string target;
    std::vector<std::pair<std::string, std::string>> headers;
    std::string body;
    bool keepalive = true;

    std::string header(std::string_view name) const {
        for (const auto& [k, v] : headers) {
            if (k.size() != name.size()) continue;
            bool same = true;
            for (std::size_t i = 0; i < k.size(); ++i) {
                char a = k[i], b = name[i];
                if (a >= 'A' && a <= 'Z') a = char(a - 'A' + 'a');
                if (b >= 'A' && b <= 'Z') b = char(b - 'A' + 'a');
                if (a != b) { same = false; break; }
            }
            if (same) return v;
        }
        return {};
    }
};

// parse reads one request out of buf. It answers how many bytes it consumed, 0
// when the request is not complete yet, and -1 when it never will be.
inline std::ptrdiff_t parse(std::string_view buf, Request* out) {
    const std::size_t head = buf.find("\r\n\r\n");
    if (head == std::string_view::npos) return 0;

    std::size_t i = 0;
    auto line = [&]() -> std::string_view {
        const std::size_t end = buf.find("\r\n", i);
        std::string_view l = buf.substr(i, end - i);
        i = end + 2;
        return l;
    };

    std::string_view first = line();
    const std::size_t m = first.find(' ');
    if (m == std::string_view::npos) return -1;
    const std::size_t t = first.find(' ', m + 1);
    if (t == std::string_view::npos) return -1;
    out->method = std::string(first.substr(0, m));
    out->target = std::string(first.substr(m + 1, t - m - 1));
    out->keepalive = first.substr(t + 1) != "HTTP/1.0";

    while (i < head + 2) {
        std::string_view l = line();
        const std::size_t c = l.find(':');
        if (c == std::string_view::npos) continue;
        std::string_view v = l.substr(c + 1);
        while (!v.empty() && (v.front() == ' ' || v.front() == '\t')) v.remove_prefix(1);
        out->headers.emplace_back(std::string(l.substr(0, c)), std::string(v));
    }

    std::size_t length = 0;
    if (const std::string n = out->header("content-length"); !n.empty()) {
        length = std::size_t(std::strtoull(n.c_str(), nullptr, 10));
    }
    if (const std::string c = out->header("connection"); c == "close" || c == "Close") {
        out->keepalive = false;
    }
    const std::size_t total = head + 4 + length;
    if (buf.size() < total) return 0;
    out->body = std::string(buf.substr(head + 4, length));
    return std::ptrdiff_t(total);
}

// answer is one response, as bytes. The reason phrase is the standard one for
// the codes an op can answer with; anything else says only its number, which is
// still a complete response line.
inline std::string answer(int status, std::string_view reason, std::string_view body,
                          bool keepalive, std::string_view type = "application/json") {
    std::string out = "HTTP/1.1 " + std::to_string(status) + " " + std::string(reason) + "\r\n";
    out += "Content-Type: " + std::string(type) + "\r\n";
    out += "Content-Length: " + std::to_string(body.size()) + "\r\n";
    out += keepalive ? "Connection: keep-alive\r\n\r\n" : "Connection: close\r\n\r\n";
    out += body;
    return out;
}

}  // namespace zip::http

#endif  // ZIP_HTTP_HPP_
