# SPDX-License-Identifier: BSD-3-Clause-Eco
#
# zipcpp links Clang's own libraries, because the parse it needs is the compiler's.

cmake_minimum_required(VERSION 3.20)
project(zipcpp CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

find_package(Clang REQUIRED CONFIG)

add_executable(zipcpp zipcpp/main.cpp zipcpp/schema.cpp)
target_include_directories(zipcpp PRIVATE ${LLVM_INCLUDE_DIRS} ${CLANG_INCLUDE_DIRS})
target_compile_definitions(zipcpp PRIVATE ${LLVM_DEFINITIONS})
target_link_libraries(zipcpp PRIVATE clangTooling clangBasic clangASTMatchers)

# The example service, so a service that is described is also a service that runs.
add_executable(accounts example/accounts.cpp)

# The schemas in the tree, and the check that each is still what its source says.
# `zipcpp -check` is to a C++ service what `zipdoc -check` is to a Go one: the
# build fails when a committed file stopped being what the declarations describe.
#
# The example is the service; the two under testdata are the fixtures the Go
# gate in cpp_test.go reads — one of every refusal, and one of every wire type
# in the order most likely to expose a wrong offset.
set(schemas
  accounts example/accounts.hpp             example/accounts.zap
  refused  zipcpp/testdata/refused.hpp      zipcpp/testdata/refused.zap
  shapes   zipcpp/testdata/shapes.hpp       zipcpp/testdata/shapes.zap)

add_custom_target(schema COMMENT "writing every schema in the tree")
add_custom_target(check COMMENT "checking every schema in the tree")
list(LENGTH schemas count)
math(EXPR last "${count} / 3 - 1")
foreach(i RANGE ${last})
  math(EXPR at "${i} * 3")
  list(GET schemas ${at} pkg)
  math(EXPR at "${at} + 1")
  list(GET schemas ${at} src)
  math(EXPR at "${at} + 1")
  list(GET schemas ${at} zap)
  set(src ${CMAKE_CURRENT_SOURCE_DIR}/${src})
  set(zap ${CMAKE_CURRENT_SOURCE_DIR}/${zap})
  add_custom_target(schema-${pkg}
    COMMAND zipcpp -package ${pkg} -o ${zap} ${src} -- -std=c++20 -xc++
    DEPENDS zipcpp ${src} COMMENT "writing ${zap}")
  add_custom_target(check-${pkg}
    COMMAND zipcpp -check -package ${pkg} -o ${zap} ${src} -- -std=c++20 -xc++
    DEPENDS zipcpp ${src} COMMENT "checking ${zap}")
  add_dependencies(schema schema-${pkg})
  add_dependencies(check check-${pkg})
endforeach()
