# Copyright 2026 Hanzo AI, Inc.
# SPDX-License-Identifier: BSD-3-Clause-Eco
#
# zip for C++: the extractor, the runtime, and a service built out of them.
#
# THE ORDER IS THE WHOLE DESIGN, and it runs on every build:
#
#   1. zipgen writes the ZAP wire runtime  (zap.hpp)
#   2. zipc-cpp reads ops.cpp with clang    (manifest.json)
#   3. zipc writes what the manifest says   (openapi.json, mcp.json, cli.json,
#                                            info.zap, info.zip.hpp)
#   4. the compiler builds the service against 1 and 3
#
# Steps 1 and 3 are Go programs and step 2 is a C++ one; all three run at BUILD
# time. What they produce is C++ and JSON, so the service that comes out has no
# Go in it and never dials one.

cmake_minimum_required(VERSION 3.20)
project(zip_cpp CXX)

set(CMAKE_CXX_STANDARD 23)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
# The pass reads the compile command for the file it parses, so a project that
# wants its ops read has to write one down.
set(CMAKE_EXPORT_COMPILE_COMMANDS ON)

set(ZIP_ROOT ${CMAKE_CURRENT_SOURCE_DIR}/.. CACHE PATH "the zip module: where zipgen and zipc live")
set(GEN ${CMAKE_CURRENT_BINARY_DIR}/gen)
file(MAKE_DIRECTORY ${GEN})

find_program(GO go REQUIRED)

# ---- the extractor ----------------------------------------------------------
#
# libclang is the compiler's own parser. Nothing else agrees with the compiler
# about what a source file says, and a manifest that disagreed would describe a
# service that does not exist.
find_library(CLANG_LIB NAMES clang clang-18 HINTS /usr/lib/llvm-18/lib /usr/lib REQUIRED)
find_path(CLANG_INCLUDE clang-c/Index.h HINTS /usr/lib/llvm-18/include /usr/include REQUIRED)

add_executable(zipc-cpp zipc/main.cpp)
target_include_directories(zipc-cpp PRIVATE include ${CLANG_INCLUDE})
target_link_libraries(zipc-cpp PRIVATE ${CLANG_LIB})

# ---- 1. the ZAP wire runtime ------------------------------------------------
add_custom_command(
    OUTPUT ${GEN}/zip/zap.hpp
    COMMAND ${CMAKE_COMMAND} -E make_directory ${GEN}/zip
    COMMAND ${GO} run ./cmd/zipgen zap -lang cpp -ns zip::zap -o ${GEN}/zip/zap.hpp
    WORKING_DIRECTORY ${ZIP_ROOT}
    COMMENT "zipgen: the ZAP wire runtime, for C++")

# ---- 2, 3 and 4, per service ------------------------------------------------
#
# The stub in step 2 exists so the FIRST parse has something to include: the
# pass reads declarations, and the generated definitions are what it is on the
# way to writing. Every later build overwrites it with the real one.
function(zip_service name)
    set(out ${GEN}/${name})
    file(MAKE_DIRECTORY ${out})
    add_custom_command(
        OUTPUT ${out}/manifest.json
        COMMAND ${CMAKE_COMMAND} -E touch ${out}/${name}.zip.hpp
        COMMAND zipc-cpp ${CMAKE_CURRENT_SOURCE_DIR}/example/${name}/ops.cpp
                -o ${out}/manifest.json
                -- -std=c++23 -I${CMAKE_CURRENT_SOURCE_DIR}/include
                   -I${CMAKE_CURRENT_SOURCE_DIR}/example -I${out} -I${GEN}
        DEPENDS zipc-cpp ${GEN}/zip/zap.hpp
                ${CMAKE_CURRENT_SOURCE_DIR}/example/${name}/ops.cpp
                ${CMAKE_CURRENT_SOURCE_DIR}/example/${name}/ops.hpp
        COMMENT "zipc: reading the ops out of ${name}")

    add_custom_command(
        OUTPUT ${out}/${name}.zip.hpp ${out}/openapi.json ${out}/mcp.json
               ${out}/cli.json ${out}/${name}.zap
        COMMAND ${GO} run ./cmd/zipc -o ${out} -lang cpp -pkg ${name} ${out}/manifest.json
        WORKING_DIRECTORY ${ZIP_ROOT}
        DEPENDS ${out}/manifest.json
        COMMENT "zipc: ${name}'s document, tools, CLI, schema and bindings")

    add_executable(${name}
        example/${name}/ops.cpp
        example/${name}/main.cpp
        ${out}/${name}.zip.hpp
        ${GEN}/zip/zap.hpp)
    target_include_directories(${name} PRIVATE include example ${out} ${GEN})
endfunction()

zip_service(info)
zip_service(store)
