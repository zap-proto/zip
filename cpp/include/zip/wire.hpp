// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: BSD-3-Clause-Eco
//
// A request and an answer, as ZAP carries them.
//
// The frame is one ZAP message: a request at flags 0x01 and an answer at 0x02,
// with method, target, protocol, headers, body and trailer at fixed byte
// offsets. Those offsets ARE the contract — they are the ones zap-proto/http
// documents and writes, so a service built here answers a Go client that never
// heard of it, and this file is where that claim is spelled out rather than
// hoped for.
//
// Nothing is decoded. A read is a bounds-checked look at bytes already in hand:
// text(reqTarget) hands back a view onto the frame, and the frame outlives the
// request that reads it.

#ifndef ZIP_WIRE_HPP_
#define ZIP_WIRE_HPP_

#include <cstdint>
#include <initializer_list>
#include <span>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

#include "zip/zap.hpp"

namespace zip::wire {

// The two frame shapes. The type rides in the upper byte of the message flags.
inline constexpr std::uint16_t kRequest = 0x01;
inline constexpr std::uint16_t kResponse = 0x02;

// Request slots. Every text or bytes slot is 8 bytes — {relOffset, length} —
// so adjacent ones are spaced 8 apart or they overlap and corrupt each other.
inline constexpr std::int64_t kReqMethod = 0;
inline constexpr std::int64_t kReqTarget = 8;
inline constexpr std::int64_t kReqProto = 16;
inline constexpr std::int64_t kReqHeaders = 24;
inline constexpr std::int64_t kReqBody = 32;
inline constexpr std::int64_t kReqTrailer = 40;
inline constexpr std::int64_t kReqSize = 48;

// Answer slots. The status is a uint16 at 0; the first text slot starts at the
// next 8-byte boundary.
inline constexpr std::int64_t kRespStatus = 0;
inline constexpr std::int64_t kRespReason = 8;
inline constexpr std::int64_t kRespProto = 16;
inline constexpr std::int64_t kRespHeaders = 24;
inline constexpr std::int64_t kRespBody = 32;
inline constexpr std::int64_t kRespTrailer = 40;
inline constexpr std::int64_t kRespSize = 48;

inline constexpr std::string_view kProto = "ZAP-HTTP/1.0";

// A header block: a count, then that many {name, value} pairs, each a length and
// its bytes, all little-endian. It is the shape zap-proto/http reads, and it is
// not JSON: a repeated name is simply a repeated pair, which is what a
// multi-value header IS, with nothing to model and nothing to escape.
inline void pair(std::string* out, std::string_view s) {
    const std::uint32_t n = std::uint32_t(s.size());
    out->push_back(char(n & 0xff));
    out->push_back(char((n >> 8) & 0xff));
    out->push_back(char((n >> 16) & 0xff));
    out->push_back(char((n >> 24) & 0xff));
    out->append(s);
}

// headers writes one block. No pairs writes nothing at all, which leaves a null
// slot — the absence a reader expects, not a block declaring zero pairs.
inline std::string headers(std::initializer_list<std::pair<std::string_view, std::string_view>> all) {
    if (all.size() == 0) return {};
    std::string out;
    const std::uint32_t n = std::uint32_t(all.size());
    out.push_back(char(n & 0xff));
    out.push_back(char((n >> 8) & 0xff));
    out.push_back(char((n >> 16) & 0xff));
    out.push_back(char((n >> 24) & 0xff));
    for (const auto& [name, value] : all) {
        pair(&out, name);
        pair(&out, value);
    }
    return out;
}

// pairs reads a header block back into names and values. A block that runs out
// mid-pair yields what it had: a header is a fact a request offered, and half a
// name is not one.
inline std::vector<std::pair<std::string, std::string>> pairs(std::string_view raw) {
    std::vector<std::pair<std::string, std::string>> out;
    auto u32 = [&](std::size_t at) -> std::uint32_t {
        return std::uint32_t(std::uint8_t(raw[at])) | (std::uint32_t(std::uint8_t(raw[at + 1])) << 8) |
               (std::uint32_t(std::uint8_t(raw[at + 2])) << 16) |
               (std::uint32_t(std::uint8_t(raw[at + 3])) << 24);
    };
    if (raw.size() < 4) return out;
    std::uint32_t n = u32(0);
    std::size_t i = 4;
    auto field = [&](std::string* into) -> bool {
        if (i + 4 > raw.size()) return false;
        const std::uint32_t len = u32(i);
        i += 4;
        if (i + len > raw.size()) return false;
        into->assign(raw.substr(i, len));
        i += len;
        return true;
    };
    for (; n > 0; --n) {
        std::string name, value;
        if (!field(&name) || !field(&value)) break;
        out.emplace_back(std::move(name), std::move(value));
    }
    return out;
}

// Request is a view onto one request frame: the message must outlive it.
struct Request {
    std::string_view method;
    std::string_view target;
    std::vector<std::pair<std::string, std::string>> headers;
    std::span<const std::uint8_t> body;
};

// read views a request frame. It answers false for a frame that is not one,
// which is the only honest answer: a peer that sent an answer where a request
// belongs is not a peer this connection can go on talking to.
inline bool read(const zap::Message& msg, Request* out) {
    if (!msg.valid() || (msg.flags() >> 8) != kRequest) return false;
    const zap::Object r = msg.root();
    out->method = r.text(kReqMethod);
    out->target = r.text(kReqTarget);
    out->headers = pairs(r.text(kReqHeaders));
    out->body = r.bytes(kReqBody);
    return true;
}

// answer builds one response frame.
inline std::vector<std::uint8_t> answer(std::uint16_t status, std::string_view reason,
                                        std::string_view headers, std::string_view body) {
    zap::Builder b(std::size_t(kRespSize) + body.size() + headers.size() + reason.size() + 64);
    zap::ObjectBuilder ob = b.start_object(kRespSize);
    ob.set_u16(kRespStatus, status);
    ob.set_text(kRespReason, reason);
    ob.set_text(kRespProto, kProto);
    ob.set_text(kRespHeaders, headers);
    ob.set_text(kRespBody, body);
    ob.set_text(kRespTrailer, "");
    ob.finish_as_root();
    return b.finish_with_flags(kResponse << 8);
}

}  // namespace zip::wire

#endif  // ZIP_WIRE_HPP_
